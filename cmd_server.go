// cmd_server.go contains the core of the VPN-server

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/google/subcommands"
	"github.com/gorilla/websocket"
	"github.com/skx/simple-vpn/config"
	"github.com/skx/simple-vpn/shared"
	"github.com/songgao/water"
)

//
// We want to make sure that we check the origin of any websocket-connections
// and bump the size of the buffers.
//
var upgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

var ip net.IP
var subnet *net.IPNet

// connections is a structure to hold data about connected
// clients
type connection struct {
	localIP  string
	remoteIP string
	name     string
}

// serverCmd is the structure for this sub-command
type serverCmd struct {
	// assigned holds a record of IPs that are available/used.
	assigned map[string]*connection

	// assignedMutex to protect access to the same.
	assignedMutex sync.Mutex

	// The MTU to use
	mtu int

	// bindHost stores the host to bind upon
	bindHost string

	// bindPort stores the port to bind upon
	bindPort int

	// The configuration file
	Config *config.Reader

	// The subnet we are using
	subnet string

	// IP of the server, within the subnet
	serverIP string
}

//
// Glue for our sub-command-library.
//
func (*serverCmd) Name() string     { return "server" }
func (*serverCmd) Synopsis() string { return "Start the VPN-server." }
func (*serverCmd) Usage() string {
	return `server :
  Launch the VPN-server.
`
}

//
// Flag setup
//
func (p *serverCmd) SetFlags(f *flag.FlagSet) {
	f.IntVar(&p.mtu, "mtu", 1280, "MTU for the tunnel")
	f.StringVar(&p.bindHost, "host", "127.0.0.1", "The IP to listen upon.")
	f.IntVar(&p.bindPort, "port", 9000, "The port to bind upon.")
}

// raiseNetworkDevice configures the link for the server.
func (p *serverCmd) raiseNetworkDevice(dev *water.Interface, mtu int) error {

	//
	// The MTU/Device as a string
	//
	mtuStr := fmt.Sprintf("%d", mtu)
	devStr := dev.Name()

	//
	// The commands we're going to execute
	//
	cmds := [][]string{
		{"ip", "link", "set", "dev", devStr, "up"},
		{"ip", "link", "set", "mtu", mtuStr, "dev", devStr},
	}

	//
	// For each command
	//
	for _, cmd := range cmds {

		//
		// Show what we're doing.
		//
		fmt.Printf("Running: '%s'\n", strings.Join(cmd, " "))

		//
		// Run the command
		//
		x := exec.Command(cmd[0], cmd[1:]...)
		x.Stdout = os.Stdout
		x.Stderr = os.Stderr
		err := x.Run()
		if err != nil {
			fmt.Printf("Failed to run %s - %s",
				strings.Join(cmd, " "), err.Error())

			return err
		}
	}
	return nil
}

// pickIP is a function which returns the IP address to use for the
// specific connecting client.
//
// Generally we pick the next unused IP in our range, but we also
// allow a hard-wired version via the configuriaton file.  Of course
// the hard-wired IP might be in use ..
func (p *serverCmd) pickIP(name string, remote string) (string, error) {
	p.assignedMutex.Lock()

	//
	// Get the fixed IP for this host, if set in the
	// configuration-file.
	//
	fixed := p.Config.Get("host_" + name)

	//
	// If that worked, and the IP is free then use it.
	//
	if fixed != "" && p.assigned[fixed] == nil {

		p.assigned[fixed] = &connection{name: name, localIP: fixed, remoteIP: remote}

		p.assignedMutex.Unlock()
		return fixed, nil
	}

	//
	// Otherwise we need to find the next free one.
	//
	for ip := ip.Mask(subnet.Mask); subnet.Contains(ip); incIP(ip) {

		s := ip.String()

		// Skip the first IP.
		if strings.HasSuffix(s, ".0") {
			continue
		}

		if p.assigned[s] == nil {
			p.assigned[s] = &connection{name: name, localIP: s, remoteIP: remote}
			p.assignedMutex.Unlock()
			return s, nil
		}
	}

	p.assignedMutex.Unlock()
	return "", fmt.Errorf("Out of IP addresses")
}

// incIP is used to increment the given IP object; it is used for iterating
// over the CIDR range the server uses for clients.
func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

//
// Entry-point.
//
func (p *serverCmd) Execute(_ context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {

	//
	// Ensure we have a configuration file.
	//
	if len(f.Args()) < 1 {
		fmt.Printf("We expect a configuration-file to be specified\n")
		return subcommands.ExitFailure
	}

	//
	// Parse the configuration file.
	//
	var err error
	p.Config, err = config.New(f.Args()[0])
	if err != nil {
		fmt.Printf("Failed to read configuration file %s\n", err.Error())
		return subcommands.ExitFailure
	}

	//
	// The subnet could be changed by the configuration-file.
	//
	p.subnet = p.Config.GetWithDefault("subnet", "10.137.248.0/24")

	//
	// Ensure we have a key
	//
	if p.Config.Get("key") == "" {
		fmt.Printf("The configuration file must define a shared-key\n")
		fmt.Printf("Please add 'key = b5499*()8304938403', or similar\n")
		return subcommands.ExitFailure

	}

	//
	// Parse the subnet we live upon.
	//
	ip, subnet, err = net.ParseCIDR(p.subnet)
	if err != nil {
		fmt.Printf("Failed to parse the CIDR range allocated to clients")
		fmt.Printf("\t%s\n", err.Error())
		return subcommands.ExitFailure
	}

	//
	// For each IP in the range we now mark the IP as free.
	//
	p.assigned = make(map[string]*connection)
	for ip := ip.Mask(subnet.Mask); subnet.Contains(ip); incIP(ip) {

		s := ip.String()

		if strings.HasSuffix(s, ".0") {
			continue
		}

		p.assigned[s] = nil

		//
		// The first IP in the range is the server's IP.
		//
		if p.serverIP == "" {
			p.serverIP = s

			fmt.Printf("VPN server has IP %s\n", p.serverIP)

			// Mark this IP as being unavailable
			p.assigned[s] = &connection{localIP: s, remoteIP: s, name: "vpn-server"}
		}
	}

	//
	// Create the tap-config
	//
	tapConfig := water.Config{
		DeviceType: water.TAP,
	}

	//
	// Set the name of the device appropriately.
	//
	// Default to `svpn` but allow the servers' configuration
	// file to override.
	//
	devName := p.Config.GetWithDefault("device", "svpn")
	tapConfig.Name = devName

	//
	// Create the tap-device
	//
	var tapDev *water.Interface
	tapDev, err = water.New(tapConfig)
	if err != nil {
		fmt.Printf("Failed to create TAP device: %s\n", err.Error())
		return subcommands.ExitFailure

	}

	//
	// Setup the server socket, with MTU, etc.
	//
	err = p.raiseNetworkDevice(tapDev, p.mtu)
	if err != nil {
		fmt.Printf("Error raising network device\n")
		fmt.Printf("\t%s\n", err.Error())
		return subcommands.ExitFailure
	}

	//
	// Prepare to bind, by building up a listening-address.
	//
	bind := fmt.Sprintf("%s:%d", p.bindHost, p.bindPort)
	fmt.Printf("Launching the server on http://%s\n", bind)

	//
	// Bind our websocket handling-function.
	//
	http.HandleFunc("/", p.serveWs)

	//
	// Now start the server.
	//
	err = http.ListenAndServe(bind, nil)
	if err != nil {
		fmt.Printf("Failed to launch our websocket-server\n")
		fmt.Printf("\t%s\n", err.Error())
		return subcommands.ExitFailure
	}

	return subcommands.ExitSuccess
}

// RemoteIP retrieves the remote IP address of the requesting HTTP-client.
//
// This is used for logging, and storing the remote (public) IP of each
// connecting clinet.
func RemoteIP(request *http.Request) string {

	//
	// Get the X-Forwarded-For header, if present.
	//
	xForwardedFor := request.Header.Get("X-Forwarded-For")

	//
	// No forwarded IP?  Then use the remote address directly.
	//
	if xForwardedFor == "" {
		ip, _, _ := net.SplitHostPort(request.RemoteAddr)
		return ip
	}

	entries := strings.Split(xForwardedFor, ",")
	address := strings.TrimSpace(entries[0])
	return (address)
}

// refreshPeers broadcasts the list of our connected peers to every
// host which is still connected.
//
// It is called when either a new client connects, or a host is reaped.
func (p *serverCmd) refreshPeers(socket shared.Socket) error {

	//
	// The hosts we'll send
	//
	var connected []string

	//
	// Populate the `connected` array with an entry for
	// each connected client.
	//
	// We'll send "IP[TAB]NAME"
	//
	p.assignedMutex.Lock()
	for _, client := range p.assigned {
		if client != nil {
			connected = append(connected,
				fmt.Sprintf("%s\t%s", client.localIP, client.name))
		}
	}
	p.assignedMutex.Unlock()

	//
	// Send the update-message
	//
	socket.BroadcastCommand("update-peers", connected)
	return nil
}

// serveWs is the handler which the VPN-clients will hit.
//
// When we get a new connection we ensure that the key matches
// the one we have configured, and if so wire it up.
//
// We create a new TUN interface for each connecting client,
// which is used to transfer data back & forth.
//
// We keep track of which clients have connected and ensure
// that we cleanup when they exit.
//
func (p *serverCmd) serveWs(w http.ResponseWriter, r *http.Request) {

	//
	// Get the name of the remote-client
	//
	name := r.URL.Query().Get("name")

	//
	// Get the shared-key
	//
	key := r.URL.Query().Get("key")

	//
	// If the key doesn't match our own then we'll abort
	//
	if p.Config.Get("key") != key {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 - Invalid/missing shared-secret"))
		return
	}

	//
	// Upgrade the websocket connection.
	//
	var err error
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[S] Error upgrading to WS: %v", err)
		return
	}

	//
	// Get the source of the connection.
	//
	ip := RemoteIP(r)
	fmt.Printf("Connection from IP:%s\n", ip)

	//
	// Assign an IP address for the connecting-client.
	//
	clientIP := ""
	clientIP, err = p.pickIP(name, ip)
	if err != nil {
		conn.Close()
		log.Printf("[S] Cannot connect new client: %s", err.Error())
		return
	}

	//
	// Show what we found.
	//
	fmt.Printf("Client '%s' [IP:%s] assigned %s\n", name, ip, clientIP)

	//
	// Create an interface for the client.
	//
	var iface *water.Interface
	iface, err = water.New(water.Config{
		DeviceType: water.TUN,
	})
	if err != nil {
		log.Printf("[S] Error creating new TUN: %v", err)
		conn.Close()
		return
	}

	//
	// Setup a socket for this connection.
	//
	socket := shared.MakeSocket(clientIP, conn, iface,
		//
		// This is the reaper-function which is invoked
		// when the client goes away, and will ensure
		// that our IP-record is removed, such that
		// we don't leak connected-counts (and also that
		// we free up the IP that was previously assigned).
		//
		func(sock shared.Socket, x string) {
			p.assignedMutex.Lock()

			// Only reap if we've not already done so.
			if p.assigned[x] != nil {
				log.Printf("Reaped dead-client with IP %s\n", x)
				p.assigned[x] = nil
			}

			p.assignedMutex.Unlock()

			//
			// Update our peers.
			//
			p.refreshPeers(sock)
		})

	//
	// When a new client connects to the server it will send
	// a "refresh" command.
	//
	// The refresh command will instruct the server to broadcast
	// the list of all know-connections to each peer.
	//
	// i.e. When host 3 joins the VPN host1 & host2 will be told
	// about it.
	//
	socket.AddCommandHandler("refresh-peers", func(args []string) error {
		return (p.refreshPeers(*socket))
	})

	//
	// Launch the "up" script, if we can.
	//
	if p.Config.Get("up") != "" {

		//
		// Setup the environment.
		//
		os.Setenv("INTERNAL_IP", clientIP)
		os.Setenv("EXTERNAL_IP", ip)
		os.Setenv("NAME", name)

		//
		// Launch the script.
		//
		cmd := p.Config.Get("up")

		x := exec.Command(cmd)
		x.Stdout = os.Stdout
		x.Stderr = os.Stderr
		err := x.Run()
		if err != nil {
			fmt.Printf("Failed to run %s - %s",
				cmd, err.Error())

		}

	}

	//
	// Send the `init` command to the client, which will ensure that
	// it configures itself.
	//
	// Arguments:
	//
	//    1.2.3.0/24 |  -> cidr-range of vpn
	//    1.2.3.4    |  -> actual assigned IP
	//    mtu        |  -> MTU
	//    1.2.3.0       -> (internal) IP of VPN-server
	//
	socket.SendCommand("init", p.subnet, clientIP, fmt.Sprintf("%d", p.mtu), p.serverIP)
	socket.Serve()
	socket.Wait()
}
