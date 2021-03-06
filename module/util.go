package module

import (
	"fmt"
	"net"
	"syscall"
	"log"
	"errors"
	"strconv"
	"strings"
	"time"
	"encoding/binary"
	"math/rand"

	"github.com/crazyboycjr/cyclone-jet-rocket/protocol"
)

// The order must be bottom up
func send(fd int, l ...protocol.Layer) error {
	var data []byte
	err := protocol.SerializeLayers(&data, l...)
	if err != nil {
		return err
	}

	layerType := l[0].LayerType()
	if layerType == protocol.LayerTypeIPv4 {
		ip := l[0].(*protocol.IPv4Packet)
		// this addr is used to choose the out interface
		tmpip := ip.DstIP.To4()
		dstip := [4]byte{tmpip[0], tmpip[1], tmpip[2], tmpip[3]}
		//log.Println("dstip = ", dstip)

		addr := &syscall.SockaddrInet4 {
			Port: 0,
			Addr: dstip,
		}
		err = syscall.Sendto(fd, data, 0, addr)
		if err != nil {
			if err == errors.New("Bad file descriptor") {
				log.Fatal("You may need to execute setcap cap_net_raw+ep `which cjr`")
			} else {
				log.Fatal("Sendto: ", err, " ", addr)
			}
		}
	}
	/*else if layerType == protocol.LayerTypeIPv6 {
		ip := l[0].(protocol.IPv6Packet)
		fd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
		addr := &syscall.SockaddrInet6 {
			Port: 0,
			Addr: ip.DstIP,
		}
		err = syscall.Sendto(fd, data, 0, addr)
		if err != nil {
			log.Fatal("Sendto:", err)
		}
	}*/
	return nil
}

func parsePortOrDie(portStr string) uint16 {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatal("Parse dport failed: ", err)
	}
	if port > 0xffff || port <= 0 {
		log.Fatal("invalid port number")
	}
	return uint16(port)
}

func parsePort(portStr string) (uint16, error) {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("Parse dport failed: %s", err.Error())
	}
	if port > 0xffff || port <= 0 {
		return 0, errors.New("invalid port number")
	}
	return uint16(port), nil
}

func parseRateOrDie(rate string) time.Duration {
	rates := strings.Split(rate, "/")
	if len(rates) > 2 {
		log.Fatal("rates parse error")
	}
	var unit string
	if len(rates) == 1 {
		unit = "s"
	} else {
		unit = rates[1]
	}
	num, err := strconv.Atoi(rates[0])
	if err != nil {
		log.Fatal("rates parse error: ", err)
	}

	wait := time.Second / time.Duration(num)
	switch unit {
		case "ms":
			wait /= 1000
		case "s":
			wait = wait
		case "min":
			wait *= 60
		case "h":
			wait *= 3600
		default:
			log.Fatal("unrecognized time unit")
	}
	return wait
}

func parseRate(rate string) (time.Duration, error) {
	rates := strings.Split(rate, "/")
	if len(rates) > 2 {
		return time.Duration(0), errors.New("rates parse error")
	}
	var unit string
	if len(rates) == 1 {
		unit = "s"
	} else {
		unit = rates[1]
	}
	num, err := strconv.Atoi(rates[0])
	if err != nil {
		return time.Duration(0), fmt.Errorf("rates parse error: %s", err.Error())
	}

	wait := time.Second / time.Duration(num)
	switch unit {
		case "ms":
			wait /= 1000
		case "s":
			wait = wait
		case "min":
			wait *= 60
		case "h":
			wait *= 3600
		default:
			return time.Duration(0), errors.New("unrecognized time unit")
	}
	return wait, nil
}

func newTCPSocketOrDie() int {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		log.Fatal("Socket: ", err)
	}
	return fd
}

func newRawSocketOrDie() int {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		log.Fatal("Socket: ", err)
	}
	return fd
}

func setBroadcastOrDie(fd int) {
	err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	if err != nil {
		log.Fatal("set sockopt error: ", err)
	}
}

func chooseIPv4(spoof string) net.IP {
	var ip net.IP
	if len(spoof) > 0 {
		if strings.Index(spoof, "/") == -1 {
			ip = net.ParseIP(spoof)
		} else {
			_, ipnet, err := net.ParseCIDR(spoof)
			if err != nil {
				log.Fatal(err)
				//return nil, err
			}
			ipmask := binary.BigEndian.Uint32(ipnet.Mask)
			up := 1
			for i := 0; i < 32; i++ {
				if ipmask >> uint(i) & 1 == 1 {
					break
				}
				up <<= 1
			}

			tmp := make([]byte, 4, 4)
			binary.BigEndian.PutUint32(tmp[0:4], binary.BigEndian.Uint32(ipnet.IP) + uint32(rand.Intn(up)))
			ip = net.IPv4(tmp[0], tmp[1], tmp[2], tmp[3])
		}
	} else {
		ip = randomIPv4()
	}
	return ip
}

func randomIPv4() net.IP {
	a, b, c, d := rand.Intn(254) + 1, rand.Intn(254) + 1, rand.Intn(254) + 1, rand.Intn(254) + 1
	return net.IPv4(byte(a), byte(b), byte(c), byte(d))
}

func packetSend(stopChan chan int, constructPacket func(CommonOption) []protocol.Layer, opts CommonOption) error {
	var fd int = -1
	var err error
	fd = newRawSocketOrDie()
	log.Println("fd = ", fd)
	if opts.IsBroadcast() {
		setBroadcastOrDie(fd)
	}

	second := time.Tick(time.Second)
	var curCount uint = 0

	var throttle <-chan time.Time
	if opts.Rate() != time.Duration(0) {
		throttle = time.Tick(opts.Rate())
	}
	count := opts.Count()

	for {
		if curCount >= count {
			break
		}
		select {
			case <-second:
				log.Printf("%d pkts sent\n", curCount)
			case <-stopChan:
				return nil
			default:
				break
		}
		if throttle != nil {
			<-throttle
		}
		// ========== construct packet ==========
		layers := constructPacket(opts)
		err = send(fd, layers...)
		if err != nil {
			// should I log.Fatal() here?
			// or bubble the error?
			return fmt.Errorf("packet send: %s", err.Error())
		}
		curCount++
	}
	return nil
}
