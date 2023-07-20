package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	networkMonitorBin = "/opt/pwx/oci/rootfs/usr/local/bin/network_monitor"
	serviceFile       = "/etc/systemd/system/portworx_network_monitor.service"
	pxctlBin          = "/opt/pwx/bin/pxctl"
)

var (
	nodes   []string
	localIP string
)

type Node struct {
	DataIp string `json:"DataIp"`
}

type Cluster struct {
	Nodes []Node `json:"Nodes"`
}

type Status struct {
	Cluster Cluster `json:"cluster"`
}

// getNodes gets the list of nodes in the cluster
func getNodes() {
	out, err := exec.Command(pxctlBin, "status", "-j").Output()
	if err != nil {
		log.Fatalf("Failed to execute pxctl status command: %v", err)
	}

	var status Status
	if err := json.Unmarshal(out, &status); err != nil {
		log.Fatalf("Failed to parse JSON output: %v", err)
	}

	nodes = make([]string, len(status.Cluster.Nodes))
	for i, node := range status.Cluster.Nodes {
		nodes[i] = node.DataIp
	}
}

// getLocalIP gets the local IP address of the node
func getLocalIP() {
	out, err := exec.Command("hostname", "-I").Output()
	if err != nil {
		log.Fatalf("Failed to execute hostname command: %v", err)
	}
	ips := strings.Fields(string(out))
	if len(ips) > 0 {
		localIP = ips[0]
	} else {
		log.Fatalf("No IP address returned by hostname command")
	}
}

// pingNode pings the node from the specified interface
func pingNode(interfaceName, node string) {

	pingCmd := fmt.Sprintf("ping -I %s -c 1 %s", interfaceName, node)
	out, err := exec.Command("bash", "-c", pingCmd).Output()
	if err != nil {
		log.Printf("Ping failed on node %s: %v", node, err)
		return
	}
	fmt.Println(string(out))

}

// removeService removes the systemd service
func removeService() {
	err := exec.Command("systemctl", "stop", "portworx_network_monitor.service").Run()
	if err != nil {
		log.Printf("Failed to stop service: %v", err)
	}
	err = exec.Command("systemctl", "disable", "portworx_network_monitor.service").Run()
	if err != nil {
		log.Printf("Failed to disable service: %v", err)
	}
	err = exec.Command("systemctl", "daemon-reload").Run()
	if err != nil {
		log.Printf("Failed to reload systemd: %v", err)
	}
	if _, err := os.Stat(serviceFile); err == nil {
		err := os.Remove(serviceFile)
		if err != nil {
			log.Printf("Failed to remove service file: %v", err)
		}
	}
}

// installService installs the systemd service
func installService(interfaceName string, frequency int, ip string) {
	serviceDefinition := `
[Unit]
Description=portworx network monitor service
After=portworx.service
StartLimitIntervalSec=0s

[Service]
Type=simple
Restart=on-failure
RestartSec=60
User=root
ExecStart=PING_CMD

[Install]
WantedBy=multi-user.target`
	log.Printf("Installing service with interface %s, frequency %d, ip %s...", interfaceName, frequency, ip)
	removeService()
	pingCmd := fmt.Sprintf("%s -interface %s -frequency %d -ip %s -r", networkMonitorBin, interfaceName, frequency, ip)
	serviceDefinition = strings.Replace(serviceDefinition, "PING_CMD", pingCmd, 1)
	err := os.WriteFile(serviceFile, []byte(serviceDefinition), 0644)
	if err != nil {
		log.Printf("Failed to write systemd service file: %v", err)
		return
	}
	log.Println("Reloading systemd...")
	err = exec.Command("systemctl", "daemon-reload").Run()
	if err != nil {
		log.Printf("Failed to reload systemd: %v", err)
		return
	}
	log.Println("Enabling service...")
	err = exec.Command("systemctl", "enable", "portworx_network_monitor.service").Run()
	if err != nil {
		log.Printf("Failed to enable service: %v", err)
		return
	}
	log.Println("Starting service...")
	err = exec.Command("systemctl", "start", "portworx_network_monitor.service").Run()
	if err != nil {
		log.Printf("Failed to start service: %v", err)
		return
	}
	out, _ := exec.Command("systemctl", "status", "portworx_network_monitor.service").Output()
	fmt.Println(string(out))
}

// main function
func main() {
	interfaceName := flag.String("interface", "", "interface to run ping from")
	frequency := flag.Int("frequency", 3, "frequency for ping")
	ip := flag.String("ip", "", "this node's ip")
	r := flag.Bool("r", false, "Directly run script without scheduling systemd service")
	flag.Parse()

	if *ip == "" {
		getLocalIP()
	} else {
		localIP = *ip
	}

	if nodes == nil {
		getNodes()
	}

	// setup logging
	log.SetOutput(os.Stdout)

	if !*r {
		installService(*interfaceName, *frequency, localIP)
	} else {
		for {
			for _, node := range nodes {
				if localIP == node {
					log.Println("skipping local node")
					continue
				}
				go pingNode(*interfaceName, node)
			}
			time.Sleep(time.Duration(*frequency) * time.Second)
		}
	}
}
