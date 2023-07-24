package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/sirupsen/logrus"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
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
func getNodes(logger *logrus.Logger) {
	out, err := exec.Command(pxctlBin, "status", "-j").Output()
	if err != nil {
		logger.Fatalf("Failed to execute pxctl status command: %v", err)
	}

	var status Status
	if err := json.Unmarshal(out, &status); err != nil {
		logger.Fatalf("Failed to parse JSON output: %v", err)
	}

	nodes = make([]string, len(status.Cluster.Nodes))
	for i, node := range status.Cluster.Nodes {
		nodes[i] = node.DataIp
	}
}

// getLocalIP gets the local IP address of the node
func getLocalIP(logger *logrus.Logger) {
	out, err := exec.Command("hostname", "-I").Output()
	if err != nil {
		logger.Fatalf("Failed to execute hostname command: %v", err)
	}
	ips := strings.Fields(string(out))
	if len(ips) > 0 {
		localIP = ips[0]
	} else {
		logger.Fatalf("No IP address returned by hostname command")
	}
}

// pingNode pings the node from the specified interface
func pingNode(ctx context.Context, interfaceName, node string, logger *logrus.Logger) (string, error) {
	pingTimeout := 5 // Set the desired timeout in seconds
	pingCmd := fmt.Sprintf("ping -I %s -c 1 -W %d %s", interfaceName, pingTimeout, node)

	cmd := exec.CommandContext(ctx, "bash", "-c", pingCmd)
	out, err := cmd.Output()
	if err != nil {
		logger.Errorf("Ping failed on node %s: %v", node, err)
		return "", err
	}
	return string(out), nil
}

// removeService removes the systemd service
func removeService(logger *logrus.Logger) {
	err := exec.Command("systemctl", "stop", "portworx_network_monitor.service").Run()
	if err != nil {
		logger.Errorf("Failed to stop service: %v", err)
	}
	err = exec.Command("systemctl", "disable", "portworx_network_monitor.service").Run()
	if err != nil {
		logger.Errorf("Failed to disable service: %v", err)
	}
	err = exec.Command("systemctl", "daemon-reload").Run()
	if err != nil {
		logger.Printf("Failed to reload systemd: %v", err)
	}
	if _, err := os.Stat(serviceFile); err == nil {
		err := os.Remove(serviceFile)
		if err != nil {
			logger.Warnf("Failed to remove service file: %v", err)
		}
	}
}

// installService installs the systemd service
func installService(interfaceName string, frequency int, ip string, logger *logrus.Logger) {
	serviceDefinition := `
[Unit]
Description=portworx network monitor service
After=portworx.service
StartLimitIntervalSec=0s

[Service]
Type=simple
RuntimeMaxSec=1800s
Restart=always
User=root
ExecStart=PING_CMD
TasksMax=200
MemoryMax=60M

[Install]
WantedBy=multi-user.target`
	logger.Printf("Installing service with interface %s, frequency %d, ip %s...", interfaceName, frequency, ip)
	removeService(logger)
	pingCmd := fmt.Sprintf("%s -interface %s -frequency %d -ip %s -r", networkMonitorBin, interfaceName, frequency, ip)
	serviceDefinition = strings.Replace(serviceDefinition, "PING_CMD", pingCmd, 1)
	err := os.WriteFile(serviceFile, []byte(serviceDefinition), 0644)
	if err != nil {
		logger.Warnf("Failed to write systemd service file: %v", err)
		return
	}
	logger.Println("Reloading systemd...")
	err = exec.Command("systemctl", "daemon-reload").Run()
	if err != nil {
		logger.Warnf("Failed to reload systemd: %v", err)
		return
	}
	logger.Println("Enabling service...")
	err = exec.Command("systemctl", "enable", "portworx_network_monitor.service").Run()
	if err != nil {
		logger.Errorf("Failed to enable service: %v", err)
		return
	}
	logger.Println("Starting service...")
	err = exec.Command("systemctl", "start", "portworx_network_monitor.service").Run()
	if err != nil {
		logger.Errorf("Failed to start service: %v", err)
		return
	}
	out, _ := exec.Command("systemctl", "status", "portworx_network_monitor.service").Output()
	fmt.Println(string(out))
}

// setupLogging sets up logging to a file and returns a logrus logger
func setupLogging() *logrus.Logger {
	logPath := "/var/lib/osd/log/nw_mon_log/nw_mon.log"
	err := os.MkdirAll(path.Dir(logPath), os.ModePerm)
	if err != nil {
		return nil
	}

	// setting logrus logger
	logger := logrus.New()

	logger.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	})

	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			logger.Errorf("Failed to close file: %v", err)
		}
	}(logger.Out.(*os.File))

	logWriter, err := rotatelogs.New(
		logPath+".%Y%m%d%H%M",
		rotatelogs.WithLinkName(logPath),          // generate softlink
		rotatelogs.WithMaxAge(24*time.Hour*60),    // max age, 60 days
		rotatelogs.WithRotationTime(time.Hour*24), // log cut, 24 hours
		rotatelogs.WithRotationSize(20*1024*1024), // enforce rotation when the log size reaches 20MB
	)
	if err != nil {
		log.Fatalf("Failed to create rotatelogs: %v", err)
	}

	writeMap := lfshook.WriterMap{
		logrus.InfoLevel:  logWriter,
		logrus.FatalLevel: logWriter,
		logrus.DebugLevel: logWriter,
		logrus.WarnLevel:  logWriter,
		logrus.ErrorLevel: logWriter,
		logrus.PanicLevel: logWriter,
	}

	lfHook := lfshook.NewHook(writeMap, &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// add hook to logger
	logger.AddHook(lfHook)

	// Output to stdout instead of the default stderr, also to the file
	logger.SetOutput(io.MultiWriter(os.Stdout, logWriter))

	return logger
}

// main function with sync.WaitGroup to wait for all goroutines to finish and prevent memory leaks
func main() {
	// setup logging
	logger := setupLogging()

	logger.Info("Set up logging for NW Monitor")
	interfaceName := flag.String("interface", "", "interface to run ping from")
	frequency := flag.Int("frequency", 3, "frequency for ping")
	ip := flag.String("ip", "", "this node's ip")
	r := flag.Bool("r", false, "Directly run script without scheduling systemd service")
	flag.Parse()

	if *ip == "" {
		getLocalIP(logger)
	} else {
		localIP = *ip
	}

	if nodes == nil {
		getNodes(logger)
	}

	if !*r {
		installService(*interfaceName, *frequency, localIP, logger)
	} else {
		for {
			ctx, cancel := context.WithCancel(context.Background())

			// context timeout for 5 minutes, for loop will restart after this to release resources
			go func() {
				<-time.After(5 * time.Minute)
				cancel()
			}()

			var wg sync.WaitGroup
			results := make(chan string, len(nodes))
			errs := make(chan error, len(nodes))
			for _, node := range nodes {
				if localIP == node {
					logger.Println("skipping local node")
					continue
				}
				wg.Add(1)
				pingContext, pingCancel := context.WithCancel(ctx)
				go func(n string) {
					defer wg.Done()
					res, err := pingNode(pingContext, *interfaceName, n, logger)
					if err != nil {
						errs <- err
					} else {
						results <- res
					}
					pingCancel()
				}(node)
			}
			go func() {
				wg.Wait()
				close(results)
				close(errs)
			}()

			// make sure all goroutines finish
			for result := range results {
				logger.Print(result)
			}
			for err := range errs {
				logger.Errorf("Ping failed: %v", err)
			}
			// If you want to wait for a certain duration between each set of pings:
			time.Sleep(time.Duration(*frequency) * time.Second)
		}
	}
}
