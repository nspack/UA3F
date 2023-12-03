package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/syslog"
	"net"
	"strings"
)

var payloadByte []byte

func main() {
	var payload string
	var port int
	flag.IntVar(&port, "p", 1080, "port")
	flag.StringVar(&payload, "f", "FFF", "User-Agent")
	flag.Parse()

	logger, err := syslog.Dial("", "", syslog.LOG_INFO, "UA3F")
	if err != nil {
		fmt.Println("syslog error:", err)
		return
	}

	printAndLog("UA3F-0.0.1", logger, syslog.LOG_INFO)
	printAndLog(fmt.Sprintf("Port: %d", port), logger, syslog.LOG_INFO)
	printAndLog(fmt.Sprintf("User-Agent: %s", payload), logger, syslog.LOG_INFO)

	payloadByte = []byte(payload)

	server, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		printAndLog(fmt.Sprintf("Listen failed: %v", err), logger, syslog.LOG_ERR)
		return
	}
	printAndLog(fmt.Sprintf("Listen on 127.0.0.1:%d", port), logger, syslog.LOG_INFO)
	for {
		client, err := server.Accept()
		if err != nil {
			printAndLog(fmt.Sprintf("Accept failed: %v", err), logger, syslog.LOG_ERR)
			continue
		}
		go process(client)
	}
}

func process(client net.Conn) {
	logger, _ := syslog.Dial("", "", syslog.LOG_INFO, "UA3F")

	if err := Socks5Auth(client); err != nil {
		printAndLog(fmt.Sprintf("auth error: %v", err), logger, syslog.LOG_ERR)
		client.Close()
		return
	}
	target, err := Socks5Connect(client)
	if err != nil {
		printAndLog(fmt.Sprintf("connect error: %v", err), logger, syslog.LOG_ERR)
		client.Close()
		return
	}
	Socks5Forward(client, target)
}

func Socks5Auth(client net.Conn) (err error) {
	buf := make([]byte, 256)
	n, err := io.ReadFull(client, buf[:2])
	if n != 2 {
		return errors.New("reading header:" + err.Error())
	}
	ver, nMethods := int(buf[0]), int(buf[1])
	if ver != 5 {
		return errors.New("invalid version")
	}
	n, err = io.ReadFull(client, buf[:nMethods])
	if n != nMethods {
		return errors.New("reading methods:" + err.Error())
	}
	n, err = client.Write([]byte{0x05, 0x00})
	if n != 2 || err != nil {
		return errors.New("write rsp:" + err.Error())
	}
	return nil
}

func Socks5Connect(client net.Conn) (net.Conn, error) {
	buf := make([]byte, 256)
	n, err := io.ReadFull(client, buf[:4])
	if n != 4 {
		return nil, errors.New("read header:" + err.Error())
	}
	ver, cmd, _, atyp := buf[0], buf[1], buf[2], buf[3]
	if ver != 5 || cmd != 1 {
		return nil, errors.New("invalid ver/cmd")
	}
	addr := ""
	switch atyp {
	case 1:
		n, err = io.ReadFull(client, buf[:4])
		if n != 4 {
			return nil, errors.New("invalid IPv4:" + err.Error())
		}
		addr = fmt.Sprintf("%d.%d.%d.%d", buf[0], buf[1], buf[2], buf[3])
	case 3:
		n, err = io.ReadFull(client, buf[:1])
		if n != 1 {
			return nil, errors.New("invalid hostname:" + err.Error())
		}
		addrLen := int(buf[0])
		n, err = io.ReadFull(client, buf[:addrLen])
		if n != addrLen {
			return nil, errors.New("invalid hostname:" + err.Error())
		}
		addr = string(buf[:addrLen])
	case 4:
		return nil, errors.New("IPv6: no supported yet")
	default:
		return nil, errors.New("invalid atyp")
	}
	n, err = io.ReadFull(client, buf[:2])
	if n != 2 {
		return nil, errors.New("read port:" + err.Error())
	}
	port := binary.BigEndian.Uint16(buf[:2])
	destAddrPort := fmt.Sprintf("%s:%d", addr, port)
	dest, err := net.Dial("tcp", destAddrPort)
	if err != nil {
		return nil, errors.New("dial dst:" + err.Error())
	}
	_, err = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if err != nil {
		dest.Close()
		return nil, errors.New("write rsp:" + err.Error())
	}
	return dest, nil
}

func Socks5Forward(client, target net.Conn) {
	forward := func(src, dest net.Conn) {
		// defer src.Close()
		defer dest.Close()
		io.Copy(src, dest)
	}

	spforward := func(dst, src net.Conn) {
		defer dst.Close()
		defer src.Close()
		parser := NewHTTPParser()
		MyCopyBuffer(dst, src, parser)
		// reader := bufio.NewReader(src)
		// bb, err := reader.Peek(14)
	}

	// fmt.Println(target.RemoteAddr())

	go forward(client, target)
	go spforward(target, client)
}

func MyCopyBuffer(dst io.Writer, src io.Reader, parser *HTTPParser) {
	logger, _ := syslog.Dial("", "", syslog.LOG_INFO, "UA3F")
	buf := make([]byte, 1024*8)
	nr, err := src.Read(buf)
	if err != nil && err != io.EOF {
		printAndLog(fmt.Sprintf("read error: %v", err), logger, syslog.LOG_ERR)
		return
	}
	hint := string(buf[0:7])
	HTTP_METHOD := []string{"GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "TRACE", "CONNECT"}
	is_http := false
	for _, v := range HTTP_METHOD {
		if strings.HasPrefix(hint, v) {
			is_http = true
			break
		}
	}
	if !is_http {
		// fmt.Println("not http connection")
		dst.Write(buf[0:nr])
		io.Copy(dst, src)
		return
	}
	for {
		httpBodyOffset, err := parser.Parse(buf[0:nr])
		for err == ErrMissingData {
			var m int
			m, err = src.Read(buf[nr:])
			if err != nil {
				printAndLog(fmt.Sprintf("read error: %v", err), logger, syslog.LOG_ERR)
				break
			}
			nr += m
			httpBodyOffset, err = parser.Parse(buf[:nr])
		}
		value, start, end := parser.FindHeader([]byte("User-Agent"))
		if value != nil && end > start {
			printAndLog(fmt.Sprintf("Hit User-Agent: %s", string(value)), logger, syslog.LOG_INFO)
			for i := start; i < end; i++ {
				buf[i] = 32
			}
			for i := range payloadByte {
				if start+i >= end {
					break
				}
				buf[start+i] = payloadByte[i]
			}
		} else {
			printAndLog("not found User-Agent", logger, syslog.LOG_INFO)
		}
		bodyLen := int(parser.ContentLength())
		if bodyLen == -1 {
			bodyLen = 0
		}

		_, ew := dst.Write(buf[0:min(httpBodyOffset+bodyLen, nr)])
		if ew != nil {
			printAndLog(fmt.Sprintf("write error: %v", ew), logger, syslog.LOG_ERR)
			break
		}
		if httpBodyOffset+bodyLen > nr {
			left := httpBodyOffset + bodyLen - nr
			for left > 0 {
				m, err := src.Read(buf[0:left])
				if err != nil {
					printAndLog(fmt.Sprintf("read error: %v", err), logger, syslog.LOG_ERR)
					break
				}
				_, ew := dst.Write(buf[0:m])
				if ew != nil {
					printAndLog(fmt.Sprintf("write error: %v", ew), logger, syslog.LOG_ERR)
					break
				}
				left -= m
			}
			nr = 0
		} else if httpBodyOffset+bodyLen < nr {
			copy(buf[0:], buf[httpBodyOffset+bodyLen:])
			nr = nr - httpBodyOffset - bodyLen
		} else {
			nr = 0
		}

		m, err := src.Read(buf[nr:])
		nr += m
		if err != nil && err != io.EOF {
			printAndLog(fmt.Sprintf("read error: %v", err), logger, syslog.LOG_ERR)
			break
		}
		if err == io.EOF {
			break
		}
	}
}

func printAndLog(mes string, logger *syslog.Writer, level syslog.Priority) {
	fmt.Println(mes)
	return
	switch level {
	case syslog.LOG_INFO:
		logger.Info(mes)
	case syslog.LOG_ERR:
		logger.Err(mes)
	case syslog.LOG_DEBUG:
		logger.Debug(mes)
	case syslog.LOG_WARNING:
		logger.Warning(mes)
	case syslog.LOG_CRIT:
		logger.Crit(mes)
	case syslog.LOG_ALERT:
		logger.Alert(mes)
	case syslog.LOG_EMERG:
		logger.Emerg(mes)
	case syslog.LOG_NOTICE:
		logger.Notice(mes)
	}
}