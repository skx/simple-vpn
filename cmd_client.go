// cmd_client.go contains the core of the VPN-client

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/google/subcommands"
	"github.com/gorilla/websocket"
	"github.com/skx/simple-vpn/config"
	"github.com/skx/simple-vpn/shared"
	"github.com/songgao/water"
)

// clientCmd is the structure for this sub-command.
//
type clientCmd struct {
	// The configuration file
	config *config.Reader
}

//
// Glue for our sub-command-library.
//
func (*clientCmd) Name() string     { return "client" }
func (*clientCmd) Synopsis() string { return "Start the VPN-client." }
func (*clientCmd) Usage() string {
	return `client :
  Launch the VPN-client.
`
}

//
// Flag setup
//
func (p *clientCmd) SetFlags(f *flag.FlagSet) {
}

func (p *clientCmd) configureClient(dev *water.Interface, ip string, subnet string, mtu int, gateway string) error {

	//
	// The MTU/Device as a string
	//
	mtuStr := fmt.Sprintf("%d", mtu)
	devStr := dev.Name()

	//
	// Ensure we have the right mask for the client IP
	//
	fmt.Printf("Client IP is %s\n", ip)
	if strings.Contains(ip, ":") {
		ip += "/128"
	} else {
		ip += "/32"
	}

	//
	// The commands we're going to execute
	//
	cmds := [][]string{
		{"ip", "link", "set", "dev", devStr, "up"},
		{"ip", "link", "set", "mtu", mtuStr, "dev", devStr},
		{"ip", "addr", "add", ip, "dev", devStr},
		{"ip", "route", "add", gateway, "dev", devStr},
		{"ip", "route", "add", subnet, "via", gateway},
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

//
// Entry-point.
//
func (p *clientCmd) Execute(_ context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {

	//
	// Ensure we have a configuration file.
	//
	if len(f.Args()) < 1 {
		fmt.Printf("We expect a configuration-file to be specified.\n")
		return subcommands.ExitFailure
	}

	//
	// Parse the configuration file.
	//
	var err error
	p.config, err = config.New(f.Args()[0])
	if err != nil {
		fmt.Printf("Failed to read the configuration file %s - %s\n", f.Args()[0], err.Error())
		return subcommands.ExitFailure
	}

	//
	// Get the end-point to which we're going to connect.
	//
	endPoint := p.config.Get("vpn")
	if endPoint == "" {
		fmt.Printf("The configuration file didn't include a vpn=... line\n")
		fmt.Printf("We don't know where to connect!  Aborting.\n")
		return subcommands.ExitFailure
	}

	//
	// Get the shared-secret.
	//
	key := p.config.Get("key")
	if key == "" {
		fmt.Printf("The configuration file didn't include key=... line\n")
		fmt.Printf("That means authentication is impossible! Aborting.\n")
		return subcommands.ExitFailure
	}

	//
	// Get our client-name
	//
	name := p.config.Get("name")
	if name == "" {
		//
		// If none is set then send the hostname.
		//
		name, _ = os.Hostname()
	}

	//
	// Add our name/key to the connection URI.
	//
	// Note that the URL might contain "?" already.  Unlikely, but
	// certainly possible.
	//
	if strings.Contains(endPoint, "?") {
		endPoint += "&"
	} else {
		endPoint += "?"
	}
	endPoint += "name=" + url.QueryEscape(name)
	endPoint += "&"
	endPoint += "key=" + url.QueryEscape(key)

	//
	// Connect to the remote host.
	//
	conn, _, err := websocket.DefaultDialer.Dial(endPoint, nil)
	if err != nil {
		fmt.Printf("Failed to connect to %s\n", endPoint)
		fmt.Printf("%s\n", err.Error())
		fmt.Printf("(The connection failed, or the key was bogus.)\n")
		return 1
	}
	defer conn.Close()

	//
	// Now we're cooking.
	//
	var iface *water.Interface

	//
	// When we're disconnected we cleanup the interface.
	//
	defer func() {
		if iface != nil {
			iface.Close()
		}
	}()

	//
	// Setup command-handlers for adding routes, etc.
	//
	socket := shared.MakeSocket("0", conn, nil, nil)

	//
	// Init is the function which is received when we connect.
	//
	// This gives us our IP, MTU, etc.
	//
	socket.AddCommandHandler("init", func(args []string) error {
		var err error

		//
		// We receive these arguments
		//
		//  1.  subnet
		//  2.  ip address
		//  3.  mtu
		//  4.  gateway
		//
		subnetStr := args[0]
		ipStr := args[1]
		mtuStr := args[2]
		gatewayStr := args[3]

		mtu, err := strconv.Atoi(mtuStr)
		if err != nil {
			fmt.Printf("MTU was not a valid int: %s\n", err.Error())
			os.Exit(1)
		}

		//
		// Create the TUN device
		//
		var waterMode water.DeviceType
		waterMode = water.TUN

		iface, err = water.New(water.Config{
			DeviceType: waterMode,
		})
		if err != nil {
			fmt.Printf("Failed to create a new TUN device: %s\n", err.Error())
			os.Exit(1)
		}

		//
		// Now configure it.
		//
		err = p.configureClient(iface, ipStr, subnetStr, mtu, gatewayStr)
		if err != nil {
			panic(err)
		}

		//
		// If we reached this point we're basically done.
		//
		// Launch the "up" script, if we can.
		//
		if p.config.Get("up") != "" {

			//
			// Setup the environment.
			//
			os.Setenv("DEVICE", iface.Name())
			os.Setenv("CLIENT_IP", ipStr)
			os.Setenv("SERVER_IP", gatewayStr)
			os.Setenv("SUBNET", subnetStr)
			os.Setenv("MTU", mtuStr)

			//
			// Launch the script.
			//
			cmd := p.config.Get("up")

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
		// Now we start shuffling packets.
		//
		log.Printf("Configured interface, the VPN is up!")
		err = socket.SetInterface(iface)
		if err != nil {
			fmt.Printf("Failed bind socket-magic to TUN device: %s\n", err.Error())
			os.Exit(1)
		}

		//
		// Send a command to the server, asking it to update all
		// clients with the list of known-peers (and their IPs).
		//
		socket.SendCommand("refresh-peers", "now")

		return nil
	})

	//
	// This function is invoked when clients join/leave the VPN.
	//
	// It is the function which is called as a result of the server
	// handling the `refresh` command that we sent at join-time.
	//
	socket.AddCommandHandler("update-peers", func(args []string) error {

		//
		// If the client has not defined a `peers` command then
		// we can just return here.
		//
		cmd := p.config.Get("peers")
		if cmd == "" {
			return nil
		}

		//
		// OK we have a command.
		//
		// We're given an array of strings such as:
		//
		//  "1.2.3.3\tsteve",
		//  "1.2.3.4\tgold",
		//
		// Convert that into a simple structure.
		//
		type Client struct {
			Name string
			IP   string
		}

		//
		// The thing we'll send
		//
		var connected []Client

		//
		// Populate, appropriately.
		//
		for _, ent := range args {
			out := strings.Split(ent, "\t")
			connected = append(connected, Client{Name: out[1], IP: out[0]})
		}

		//
		// Convert to JSON.
		//
		obj, err := json.Marshal(connected)
		if err != nil {
			fmt.Printf("Failed to convert object to JSON: %s\n", err.Error())
			return err
		}

		x := exec.Command(cmd)
		x.Stdin = bytes.NewBuffer(obj)
		x.Stdout = os.Stdout
		x.Stderr = os.Stderr
		err = x.Run()
		if err != nil {
			fmt.Printf("Failed to run %s - %s",
				cmd, err.Error())
			return err
		}
		return nil
	})

	socket.Serve(false)
	socket.Wait()

	return subcommands.ExitSuccess
}
