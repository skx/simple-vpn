# Simple-VPN

This project is a VPN-server, written in golang, using websockets as a transport.  The idea is that multiple-nodes each connect to a central VPN-server, and once connected they can talk to _each other_ securely, regardless of NAT and location.

The following image illustrates the expected setup:

* Three hosts each connect to the central VPN host.
* Once they're connected any host can talk to the others, securely & privately.

![Screenshot](_media/vpn.png)

While you could use this software to mask your laptop's IP while traveling, instead showing the IP of the VPN-server as being the source of connections this is _not_ the expected use-case.

This software is primarily designed to allow virtual machines, located at different hosting companies, to securely and privately communicate with each other, and present services over "local" IP addresses.

> Of course the VPN will be a single point of failure, but being a simple service and easily deployed it should be trivial to spin up a replacement in a hurry.


## Encryption

The VPN-server __does not__ implement any kind of encryption itself, nor does it handle access-control beyond a shared-secret.

Is this insane?  Actually no.  I'd rather add no encryption than badly implemented encryption, and because we're using websockets we can prevent eavesdroppers and man-in-the-middle attacks via the use of TLS.

In practice this is secure.


## Setup - Server

First of all install the binary:

    go get ..

Now you're ready to configure the VPN-server.  Configuring a VPN server requires two things:

* The `simple-vpn` binary to be running in server-mode.
* Your webserver to proxy (websocket) requests to it.
  * You __must__ setup SSL to avoid sniffing.

To launch the server you simply need to run:

     # simple-vpn server [-verbose]

To proxy traffic to this server, via `nginx`, you could have a configuration file like this:

    server {
        server_name vpn.example.com;
        listen [::]:443  default ipv6only=off ssl;

        ssl on;
        ssl_certificate      /etc/lets.encrypt/ssl/vpn.example.com.full;
        ssl_certificate_key  /etc/lets.encrypt/ssl/vpn.example.com.key;
        ssl_dhparam /etc/nginx/ssl/dhparam.pem;

        ssl_prefer_server_ciphers on;
        ssl_protocols TLSv1 TLSv1.1 TLSv1.2;
        ssl_ciphers 'ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES256-GCM-SHA384:DHE-RSA-AES128-GCM-SHA256:DHE-DSS-AES128-GCM-SHA256:kEDH+AESGCM:ECDHE-RSA-AES128-SHA256:ECDHE-ECDSA-AES128-SHA256:ECDHE-RSA-AES128-SHA:ECDHE-ECDSA-AES128-SHA:ECDHE-RSA-AES256-SHA384:ECDHE-ECDSA-AES256-SHA384:ECDHE-RSA-AES256-SHA:ECDHE-ECDSA-AES256-SHA:DHE-RSA-AES128-SHA256:DHE-RSA-AES128-SHA:DHE-DSS-AES128-SHA256:DHE-RSA-AES256-SHA256:DHE-DSS-AES256-SHA:DHE-RSA-AES256-SHA:ECDHE-RSA-DES-CBC3-SHA:ECDHE-ECDSA-DES-CBC3-SHA:AES128-GCM-SHA256:AES256-GCM-SHA384:AES128-SHA256:AES256-SHA256:AES128-SHA:AES256-SHA:AES:CAMELLIA:DES-CBC3-SHA:!aNULL:!eNULL:!EXPORT:!DES:!RC4:!MD5:!PSK:!aECDH:!EDH-DSS-DES-CBC3-SHA:!EDH-RSA-DES-CBC3-SHA:!KRB5-DES-CBC3-SHA';
        add_header Strict-Transport-Security "max-age=31536000";

        proxy_buffering    off;
        proxy_buffer_size  128k;
        proxy_buffers 100  128k;

        ## VPN server ..
        location /vpn {

           proxy_pass http://127.0.0.1:9000;
           proxy_http_version 1.1;
           proxy_set_header Upgrade $http_upgrade;
           proxy_set_header Connection "upgrade";
           proxy_read_timeout 86400;

           proxy_connect_timeout 43200000;

           tcp_nodelay on;
       }
    }

* You don't need to dedicate a complete virtual host to the VPN-server, a single "location" is sufficient.
  * In this example we've chosen https://vpn.example.com/vpn to pass through to `simple-vpn`.


## Setup - Clients

Install the binary upon the client hosts you wish to link, and launch them like so:

    $ simple-vpn client https://vpn.example.com/vpn

The argument here is the end-point which you configured your webserver to proxy.


## Advanced

* Static IPs
* Routing
* Proxying


Steve
--
