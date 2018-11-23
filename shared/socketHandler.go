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
type reap func(string)

var lastCommandId uint64 = 0

var defaultMac = [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

var macTable map[MacAddr]*Socket = make(map[MacAddr]*Socket)
var macLock sync.RWMutex
var allSockets = make(map[*Socket]*Socket)
var allSocketsLock sync.RWMutex

func FindSocketByMAC(mac MacAddr) *Socket {
	macLock.RLock()
	defer macLock.RUnlock()
	return macTable[mac]
}

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

type CommandHandler func(args []string) error

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

func (s *Socket) AddCommandHandler(command string, handler CommandHandler) {
	s.handlers[command] = handler
}

func (s *Socket) Wait() {
	s.wg.Wait()
}

func (s *Socket) rawSendCommand(commandId string, command string, args ...string) error {
	return s.WriteMessage(websocket.TextMessage,
		[]byte(fmt.Sprintf("%s|%s|%s", commandId, command, strings.Join(args, "|"))))
}

func (s *Socket) SendCommand(command string, args ...string) error {
	return s.rawSendCommand(fmt.Sprintf("%d", atomic.AddUint64(&lastCommandId, 1)), command, args...)
}

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

func (s *Socket) closeDone() {
	s.wg.Done()
	s.Close()

	// If we have a reap-function.
	//  and we've not invoked it already
	// Then do so.
	if s.reaper != nil && s.reaped == false {
		// invoke
		s.reaper(s.clientIP)

		// avoid repeats.
		s.reaped = true
	}
}

func (s *Socket) SetInterface(iface *water.Interface) error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()

	if s.iface != nil {
		return errors.New("Cannot re-define interface. Already set.")
	}
	s.iface = iface
	s.tryServeIfaceRead()
	return nil
}

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

func (s *Socket) Serve() {
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
			msgType, msg, err := s.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
					log.Printf("[%s] Error reading packet from WS: %v\n", s.clientIP, err)
				}
				return
			}

			// IP data
			if msgType == websocket.BinaryMessage {

				if len(msg) >= 14 {
					s.setMACFrom(msg)

					dest := GetDestMAC(msg)
					isUnicast := MACIsUnicast(dest)

					var sd *Socket
					if isUnicast {
						sd = FindSocketByMAC(dest)
						if sd != nil {
							sd.WriteMessage(websocket.BinaryMessage, msg)
							continue
						}
					} else {
						BroadcastMessage(websocket.BinaryMessage, msg, s)
					}
				}

				if s.iface == nil {
					continue
				}
				s.iface.Write(msg)
			} else if msgType == websocket.TextMessage {
				// in-band stuff.

				str := strings.Split(string(msg), "|")
				if len(str) < 2 {
					log.Printf("[%s] Invalid in-band command structure", s.clientIP)
					continue
				}

				commandId := str[0]
				commandName := str[1]
				if commandName == "reply" {
					commandResult := "N/A"
					if len(str) > 2 {
						commandResult = str[2]
					}
					log.Printf("[%s] Got command reply ID %s: %s", s.clientIP, commandId, commandResult)
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

				s.rawSendCommand(commandId, "reply", fmt.Sprintf("%v", err == nil))
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
