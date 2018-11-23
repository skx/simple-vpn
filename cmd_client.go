// cmd_client.go contains the core of the VPN-client

package main

import (
	"context"
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
// Currently there are no members.
type clientCmd struct {
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
	// The commands we're going to execute
	//
	cmds := [][]string{
		{"ip", "link", "set", "dev", devStr, "up"},
		{"ip", "link", "set", "mtu", mtuStr, "dev", devStr},
		{"ip", "addr", "add", ip + "/32", "dev", devStr},
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
	cnf, err := config.New(f.Args()[0])
	if err != nil {
		fmt.Printf("Failed to read the configuration file %s - %s\n", f.Args()[0], err.Error())
		return subcommands.ExitFailure
	}

	//
	// Get the end-point to which we're going to connect.
	//
	endPoint := cnf.Get("vpn")
	if endPoint == "" {
		fmt.Printf("The configuration file didn't include a vpn=... line\n")
		fmt.Printf("We don't know where to connect!  Aborting.\n")
		return subcommands.ExitFailure
	}

	//
	// Get the shared-secret.
	//
	key := cnf.Get("key")
	if key == "" {
		fmt.Printf("The configuration file didn't include key=... line\n")
		fmt.Printf("That means authentication is impossible! Aborting.\n")
		return subcommands.ExitFailure
	}

	//
	// Get our client-name
	//
	name := cnf.Get("name")
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

		var waterMode water.DeviceType
		waterMode = water.TUN

		iface, err = water.New(water.Config{
			DeviceType: waterMode,
		})
		if err != nil {
			fmt.Printf("Failed to create a new TUN device: %s\n", err.Error())
			os.Exit(1)
		}

		log.Printf("Opened %s", iface.Name())

		err = p.configureClient(iface, ipStr, subnetStr, mtu, gatewayStr)
		if err != nil {
			panic(err)
		}

		log.Printf("Configured interface. Starting operations.")
		err = socket.SetInterface(iface)
		if err != nil {
			fmt.Printf("Failed bind socket-magic to TUN device: %s\n", err.Error())
			os.Exit(1)
		}

		return nil
	})
	socket.Serve()
	socket.Wait()

	return subcommands.ExitSuccess
}
