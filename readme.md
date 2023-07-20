# Portworx Network Monitor (Golang version)

The Network Monitor is a service written in Go that monitors the network status of your nodes in a cluster environment.

## Authors 
- Aditya Kulkarni (Python version)
- Carlos Alvarado (Golang version)

## Features

- Monitors the network status by sending a ping to all nodes in a cluster.
- Handles service installation, removing any existing service and installing a new one.
- Allows for direct script execution without scheduling a systemd service.

## Building

To build this project, you can use the standard Go build command:

```bash
CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -o network_monitor
```

This will produce an executable named `network_monitor`.

## Usage

Here's a quick overview of the command-line flags:

- `-interface`: Specify the interface to run the ping from.
- `-frequency`: Set the frequency for the ping in seconds.
- `-ip`: Specify this node's IP.
- `-r`: Run the script directly without scheduling a systemd service.

An example usage might look like (installing the service):

```bash
sudo mv network_monitor /opt/pwx/oci/rootfs/usr/local/bin/
cd /opt/pwx/oci/rootfs/usr/local/bin/
./network_monitor -interface ens160 -frequency 5 -ip 10.235.175.225
```

Logs are written to `/var/lib/osd/log/nw_mon_log` that have rotation enabled. 
The log file is rotated every day, and it retains for 60 days. 
It also sends the output to stdout.
