// shared/socketHandler.go contains code to route traffic over
// the socket.
//
// When a client connects to the VPN server their local tun device
// is one end of the socket, and the WS-connection is the other.
//
// For the server we have an array of such things, and we handle
// traffic by sending to the "correct" socket by MAC address - except
// in the case of IPv6 where we broadcast.
//
// IPv6 behaviour could, and should, be improved.  But handling router
// advertisements, neighbour solicitations, etc, is hard.  Better to
// keep it simple.  Keep it secret.  Keep it safe.

package shared

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/songgao/water"
)

// Type of reaping function
type reap func(Socket, string)

var lastCommandID uint64

var defaultMac = [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

var macTable = make(map[MacAddr]*Socket)

var macLock sync.RWMutex
var allSockets = make(map[*Socket]*Socket)
var allSocketsLock sync.RWMutex

// FindSocketByMAC finds the correct socket, by looking for the
// specified MAC address.
func FindSocketByMAC(mac MacAddr) *Socket {
	macLock.RLock()
	defer macLock.RUnlock()
	return macTable[mac]
}

// BroadcastMessage sends the given data over all sockets.
func BroadcastMessage(msgType int, data []byte, skip *Socket) {
	allSocketsLock.RLock()
	targetList := make([]*Socket, 0)
	for _, v := range allSockets {
		if v == skip {
			continue
		}
		targetList = append(targetList, v)
	}
	allSocketsLock.RUnlock()

	for _, v := range targetList {
		v.WriteMessage(msgType, data)
	}
}

// CommandHandler is the signature of a function which can be
// triggered via a command over our websocket connection.
// We use if for `init`.
type CommandHandler func(args []string) error

// Socket holds state about our connection.
type Socket struct {
	clientIP      string
	conn          *websocket.Conn
	iface         *water.Interface
	writeLock     *sync.Mutex
	wg            *sync.WaitGroup
	handlers      map[string]CommandHandler
	closechan     chan bool
	closechanopen bool
	mac           MacAddr
	reaper        reap
	reaped        bool
}

// MakeSocket is our constructor.  It ties a websocket connection to
// an interface connection.
func MakeSocket(clientIP string, conn *websocket.Conn, iface *water.Interface, fn reap) *Socket {
	return &Socket{
		clientIP:      clientIP,
		conn:          conn,
		iface:         iface,
		writeLock:     &sync.Mutex{},
		wg:            &sync.WaitGroup{},
		handlers:      make(map[string]CommandHandler),
		closechan:     make(chan bool),
		closechanopen: true,
		mac:           defaultMac,
		reaper:        fn,
	}
}

// AddCommandHandler binds a function-name to a handler, which is
// used in our websocket connection.
func (s *Socket) AddCommandHandler(command string, handler CommandHandler) {
	s.handlers[command] = handler
}

// Wait waits for our socket to be done.
func (s *Socket) Wait() {
	s.wg.Wait()
}

// rawSendCommand sends a "command" over our websocket link
func (s *Socket) rawSendCommand(commandID string, command string, args ...string) error {
	return s.WriteMessage(websocket.TextMessage,
		[]byte(fmt.Sprintf("%s|%s|%s", commandID, command, strings.Join(args, "|"))))
}

// SendCommand sends a "command" over our websocket link
func (s *Socket) SendCommand(command string, args ...string) error {
	return s.rawSendCommand(fmt.Sprintf("%d", atomic.AddUint64(&lastCommandID, 1)), command, args...)
}

// BroadcastCommand sends the given data over all sockets.
func (s *Socket) BroadcastCommand(command string, args []string) error {
	allSocketsLock.RLock()
	targetList := make([]*Socket, 0)
	for _, v := range allSockets {
		targetList = append(targetList, v)
	}
	allSocketsLock.RUnlock()

	for _, v := range targetList {
		v.SendCommand(command, args...)
	}
	return nil
}

// WriteMessage sends data over our socket.
func (s *Socket) WriteMessage(msgType int, data []byte) error {
	s.writeLock.Lock()
	err := s.conn.WriteMessage(msgType, data)
	s.writeLock.Unlock()
	if err != nil {
		log.Printf("[%s] Error writing packet to WS: %v", s.clientIP, err)
		s.Close()
	}
	return err
}

// closeDown closes a websocket and interface.
// It invokes the call-back "reap" function too.
func (s *Socket) closeDone() {
	s.wg.Done()
	s.Close()

	// If we have a reap-function.
	//  and we've not invoked it already
	// Then do so.
	if s.reaper != nil && s.reaped == false {
		// invoke
		s.reaper(*s, s.clientIP)

		// avoid repeats.
		s.reaped = true
	}
}

// SetInterface sets the given network-interface to be associated with us.
func (s *Socket) SetInterface(iface *water.Interface) error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()

	if s.iface != nil {
		return errors.New("cannot re-define interface. Already set")
	}
	s.iface = iface
	s.tryServeIfaceRead()
	return nil
}

// setMACFrom updates the MAC-address for this socket, unless already set.
func (s *Socket) setMACFrom(msg []byte) {
	srcMac := GetSrcMAC(msg)
	if !MACIsUnicast(srcMac) || srcMac == s.mac {
		return
	}

	macLock.Lock()
	defer macLock.Unlock()
	if s.mac != defaultMac {
		delete(macTable, s.mac)
	}
	s.mac = srcMac
	macTable[srcMac] = s
}

// Close closes our interface and websocket.
func (s *Socket) Close() {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	s.conn.Close()
	if s.iface != nil {
		s.iface.Close()
	}
	if s.closechanopen {
		s.closechanopen = false
		close(s.closechan)
	}
	if s.mac != defaultMac {
		macLock.Lock()
		delete(macTable, s.mac)
		s.mac = defaultMac
		macLock.Unlock()
	}

	allSocketsLock.Lock()
	delete(allSockets, s)
	allSocketsLock.Unlock()
}

// tryServeIfaceRead handles reading from our interface
func (s *Socket) tryServeIfaceRead() {
	if s.iface == nil {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.closeDone()

		packet := make([]byte, 2000)

		for {
			n, err := s.iface.Read(packet)
			if err != nil {
				log.Printf("[%s] Error reading packet from tun: %v", s.clientIP, err)
				return
			}

			err = s.WriteMessage(websocket.BinaryMessage, packet[:n])
			if err != nil {
				return
			}
		}
	}()
}

// Serve is the main-driver which never returns
// Handle proxying data back and forth..
func (s *Socket) Serve(ipv6 bool) {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	s.tryServeIfaceRead()

	allSocketsLock.Lock()
	allSockets[s] = s
	allSocketsLock.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.closeDone()

		for {
			//
			// Read message over the WS connection,
			//
			msgType, msg, err := s.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
					log.Printf("[%s] Error reading packet from WS: %v\n", s.clientIP, err)
				}
				return
			}

			//
			// The websocket connection gets two things:
			//
			// Binary network-data, or inline-commands.
			//
			// Here we handle binary stuff.
			//
			if msgType == websocket.BinaryMessage {

				if len(msg) >= 14 {

					//
					// IPv4 traffic involves routing "correctly".
					//
					if ipv6 == false {

						//
						// Look at the packet-data to get the src/dsg.
						//
						s.setMACFrom(msg)
						dest := GetDestMAC(msg)

						//
						// Is this unicast traffic?
						//
						isUnicast := MACIsUnicast(dest)

						//
						// If unicast - sending to one destination - then
						// lookup the socket and send it there.
						//
						var sd *Socket
						if isUnicast {

							//
							// If we find the destination, then send it.
							//
							sd = FindSocketByMAC(dest)
							if sd != nil {
								sd.WriteMessage(websocket.BinaryMessage, msg)
								continue
							}
						} else {
							//
							// OK multicast/broadcast.
							//
							// Send to everybody.
							//
							BroadcastMessage(websocket.BinaryMessage, msg, s)
						}
					} else {

						//
						// IPv6 traffic is just broadcast as-is.
						//
						BroadcastMessage(websocket.BinaryMessage, msg, s)
					}
				}

				if s.iface == nil {
					continue
				}
				s.iface.Write(msg)

			} else if msgType == websocket.TextMessage {

				// in-band messages over the WS link

				str := strings.Split(string(msg), "|")
				if len(str) < 2 {
					log.Printf("[%s] Invalid in-band command structure", s.clientIP)
					continue
				}

				commandID := str[0]
				commandName := str[1]
				if commandName == "reply" {
					commandResult := "N/A"
					if len(str) > 2 {
						commandResult = str[2]
					}
					log.Printf("[%s] Got command reply ID %s: %s", s.clientIP, commandID, commandResult)
					continue
				}

				handler := s.handlers[commandName]
				if handler == nil {
					err = errors.New("Unknown command")
				} else {
					err = handler(str[2:])
				}
				if err != nil {
					log.Printf("[%s] Error in in-band command %s: %v", s.clientIP, commandName, err)
				}

				s.rawSendCommand(commandID, "reply", fmt.Sprintf("%v", err == nil))
			}
		}
	}()

	timeout := time.Duration(30) * time.Second

	lastResponse := time.Now()
	s.conn.SetPongHandler(func(msg string) error {
		lastResponse = time.Now()
		return nil
	})

	s.wg.Add(1)
	go func() {
		defer s.closeDone()

		for {
			select {
			case <-time.After(timeout / 2):
				if time.Now().Sub(lastResponse) > timeout {
					log.Printf("[%s] Ping timeout", s.clientIP)
					return
				}
				err := s.WriteMessage(websocket.PingMessage, []byte{})
				if err != nil {
					return
				}
			case <-s.closechan:
				return
			}
		}
	}()
}
