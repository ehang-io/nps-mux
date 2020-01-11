package nps_mux

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

var conn1 net.Conn
var conn2 net.Conn
var clientIp = "172.18.0.5"
var serverIp = "172.18.0.2"
var appIp = "172.18.0.3"
var userIp = "172.18.0.4"
var bridgePort = "9999"
var appPort = "9998"
var serverPort = "9997"
var appResultFileName = "app.txt"
var userResultFileName = "user.txt"
var serverResultFileName = "server.txt"
var clientResultFileName = "client.txt"
var dockerNetWorkName = "test"
var network = "172.18.0.0/16"
var fileSavePath = "/usr/src/myapp/"

func TestMux(t *testing.T) {
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	createNetwork(dockerNetWorkName, network)
	go runDocker("server", dockerNetWorkName, serverIp, "TestServer", pwd)
	go runDocker("client", dockerNetWorkName, clientIp, "TestClient", pwd)
	go runDocker("app", dockerNetWorkName, appIp, "TestApp", pwd)
	runDocker("user", dockerNetWorkName, userIp, "TestUser", pwd)
	stopDocker("server")
	stopDocker("client")
	stopDocker("app")
	deleteNetwork(dockerNetWorkName)
}

func writeResult(values []float64, outfile string) error {
	file, err := os.Create(fileSavePath + outfile)
	if err != nil {
		fmt.Println("writer", err)
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, v := range values {
		writer.WriteString(fmt.Sprintf("%.2f", v))
		writer.WriteString("\n")
		writer.Flush()
	}
	return err
}

func TestServer(t *testing.T) {
	tc, err := NewTrafficControl(serverIp)
	if err != nil {
		t.Fatal(err, tc)
	}
	// do some tc settings
	tc.delay("add", "50ms", "10ms", "10%")
	tc.Run()
	// start bridge
	bridgeListener, err := net.Listen("tcp", serverIp+":"+bridgePort)
	if err != nil {
		t.Fatal(err)
	}
	// wait for client
	clientBridgeConn, err := bridgeListener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer clientBridgeConn.Close()
	// new mux
	mux := NewMux(clientBridgeConn, "tcp")
	// start server port
	serverListener, err := net.Listen("tcp", serverIp+":"+serverPort)
	if err != nil {
		t.Fatal(err)
	}
	defer serverListener.Close()
	for {
		// accept user connection
		userConn, err := serverListener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go func(userConn net.Conn) {
			// create a conn from mux
			clientConn, err := mux.NewConn()
			if err != nil {
				t.Fatal(err)
			}
			go io.Copy(userConn, clientConn)
			go func() {
				ticker := time.NewTicker(time.Second * 10)
				for {
					select {
					case <-ticker.C:
						fmt.Println(mux.bw.Get()/1024/1024, math.Float64frombits(atomic.LoadUint64(&mux.latency)))
						writeResult([]float64{
							mux.bw.Get() / 1024 / 1024,
							math.Float64frombits(atomic.LoadUint64(&mux.latency)),
						}, serverResultFileName)
					}
				}
			}()
			io.Copy(clientConn, userConn)
		}(userConn)
	}
}
func TestClient(t *testing.T) {
	tc, err := NewTrafficControl(clientIp)
	if err != nil {
		t.Fatal(err, tc)
	}
	// do some tc settings
	tc.delay("add", "30ms", "10ms", "5%")
	tc.Run()
	serverConn, err := net.Dial("tcp", serverIp+":"+bridgePort)
	if err != nil {
		t.Fatal(err)
	}
	// crete mux by serverConn
	mux := NewMux(serverConn, "tcp")
	// start accept user connection
	for {
		userConn, err := mux.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go func(userConn net.Conn) {
			// connect to app
			appConn, err := net.Dial("tcp", appIp+":"+appPort)
			if err != nil {
				t.Fatal()
			}
			defer appConn.Close()
			defer userConn.Close()
			go io.Copy(userConn, appConn)
			go func() {
				time.Sleep(time.Second * 10)
				writeResult([]float64{
					mux.bw.Get() / 1024 / 1024,
					math.Float64frombits(atomic.LoadUint64(&mux.latency)),
				}, clientResultFileName)
			}()
			io.Copy(appConn, userConn)
		}(userConn)
	}
}
func TestApp(t *testing.T) {
	tc, err := NewTrafficControl(appIp)
	if err != nil {
		t.Fatal(err, tc)
	}
	// do some tc settings
	tc.delay("add", "40ms", "5ms", "1%")
	tc.Run()
	appListener, err := net.Listen("tcp", appIp+":"+appPort)
	if err != nil {
		t.Fatal(err)
	}
	for {
		userConn, err := appListener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go func(userConn net.Conn) {
			b := bytes.Repeat([]byte{0}, 1024)
			startTime := time.Now()
			// send 100mb data to user
			for i := 0; i < 1024*100; i++ {
				n, err := userConn.Write(b)
				if err != nil {
					t.Fatal(err)
				}
				if n != 1024 {
					t.Fatal("the write len is not right")
				}
			}
			// send bandwidth
			writeBw := 100 / time.Now().Sub(startTime).Seconds()
			// get 100md from user
			startTime = time.Now()
			readLen := 0
			for i := 0; i < 1024*1000; i++ {
				n, err := userConn.Read(b)
				if err != nil {
					log.Fatal(err)
				}
				readLen += n
				if readLen == 1024*1024*100 {
					break
				}
			}
			if readLen != 1024*1024*100 {
				t.Fatal("the read len is not right")
			}
			// read bandwidth
			readBw := 100 / time.Now().Sub(startTime).Seconds()
			// save result
			err := writeResult([]float64{writeBw, readBw}, appResultFileName)
			if err != nil {
				t.Fatal(err)
			}
		}(userConn)
	}
}
func TestUser(t *testing.T) {
	tc, err := NewTrafficControl(userIp)
	if err != nil {
		t.Fatal(err, tc)
	}
	// do some tc settings
	tc.delay("add", "20ms", "40ms", "50%")
	tc.Run()
	appConn, err := net.Dial("tcp", serverIp+":"+serverPort)
	if err != nil {
		t.Fatal(err)
	}
	b := bytes.Repeat([]byte{0}, 1024)
	startTime := time.Now()
	// get 100md from app
	readLen := 0
	for i := 0; i < 1024*10000; i++ {
		n, err := appConn.Read(b)
		if err != nil {
			log.Fatal(err)
		}
		readLen += n
		if readLen == 1024*1024*100 {
			break
		}
	}
	if readLen != 1024*1024*100 {
		t.Fatal("the read len is not right", readLen, 1024*1024*100)
	}
	// read bandwidth
	readBw := 100 / time.Now().Sub(startTime).Seconds()
	// send 100mb data to app
	startTime = time.Now()
	for i := 0; i < 1024*100; i++ {
		n, err := appConn.Write(b)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1024 {
			t.Fatal("the write len is not right")
		}
	}
	// send bandwidth
	writeBw := 100 / time.Now().Sub(startTime).Seconds()
	// save result
	err = writeResult([]float64{readBw, writeBw}, userResultFileName)
	if err != nil {
		t.Fatal(err)
	}
}
func TestNewMux2(t *testing.T) {
	tc, err := NewTrafficControl("")
	if err != nil {
		t.Fatal(err)
	}
	err = tc.RunNetRangeTest(func() {
		server(tc.Eth.EthAddr)
		client(tc.Eth.EthAddr)
		time.Sleep(time.Second * 3)
		rate := NewRate(1024 * 1024 * 3)
		rate.Start()
		conn2 = NewRateConn(rate, conn2)
		go func() {
			m2 := NewMux(conn2, "tcp")
			for {
				c, err := m2.Accept()
				if err != nil {
					log.Println(err)
					continue
				}
				go func() {
					c.Write(bytes.Repeat([]byte{0}, 1024*1024*100))
					c.Close()
				}()
			}
		}()

		m1 := NewMux(conn1, "tcp")
		tmpCpnn, err := m1.NewConn()
		if err != nil {
			log.Println("nps new conn err ", err)
			return
		}
		buf := make([]byte, 1024*1024)
		var count float64
		count = 0
		start := time.Now()
		for {
			n, err := tmpCpnn.Read(buf)
			count += float64(n)
			log.Println(m1.bw.Get())
			log.Println(uint32(math.Float64frombits(atomic.LoadUint64(&m1.latency))))
			if err != nil {
				log.Println(err)
				break
			}
		}
		log.Println("now rate", count/time.Now().Sub(start).Seconds()/1024/1024)
	})
	log.Println(err.Error())
}
func TestNewMux(t *testing.T) {
	go func() {
		http.ListenAndServe("0.0.0.0:8889", nil)
	}()
	server("")
	client("")
	time.Sleep(time.Second * 3)
	go func() {
		m2 := NewMux(conn2, "tcp")
		for {
			log.Println("npc starting accept")
			c, err := m2.Accept()
			if err != nil {
				log.Println(err)
				continue
			}
			log.Println("npc accept success ")
			c2, err := net.Dial("tcp", "127.0.0.1:80")
			if err != nil {
				log.Println(err)
				c.Close()
				continue
			}
			//c2.(*net.TCPConn).SetReadBuffer(0)
			//c2.(*net.TCPConn).SetReadBuffer(0)
			go func(c2 net.Conn, c *conn) {
				go func() {
					_, err = io.Copy(c2, c)
					if err != nil {
						c2.Close()
						c.Close()
						//logs.Warn("close npc by copy from nps", err, c.connId)
					}
				}()
				_, err = io.Copy(c, c2)
				if err != nil {
					c2.Close()
					c.Close()
					//logs.Warn("close npc by copy from server", err, c.connId)
				}
			}(c2, c.(*conn))
		}
	}()

	go func() {
		m1 := NewMux(conn1, "tcp")
		l, err := net.Listen("tcp", "127.0.0.1:7777")
		if err != nil {
			log.Println(err)
		}
		for {
			log.Println("nps starting accept")
			conns, err := l.Accept()
			if err != nil {
				log.Println(err)
				continue
			}
			//conns.(*net.TCPConn).SetReadBuffer(0)
			//conns.(*net.TCPConn).SetReadBuffer(0)
			log.Println("nps accept success starting New conn")
			tmpCpnn, err := m1.NewConn()
			if err != nil {
				log.Println("nps New conn err ", err)
				continue
			}
			//logs.Warn("nps New conn success ", tmpCpnn.connId)
			go func(tmpCpnn *conn, conns net.Conn) {
				go func() {
					_, err := io.Copy(tmpCpnn, conns)
					if err != nil {
						conns.Close()
						tmpCpnn.Close()
						//logs.Warn("close nps by copy from user", tmpCpnn.connId, err)
					}
				}()
				time.Sleep(time.Second)
				_, err = io.Copy(conns, tmpCpnn)
				if err != nil {
					conns.Close()
					tmpCpnn.Close()
					//logs.Warn("close nps by copy from npc ", tmpCpnn.connId, err)
				}
			}(tmpCpnn, conns)
		}
	}()

	//go NewLogServer()
	time.Sleep(time.Second * 5)
	//for i := 0; i < 1; i++ {
	//	go test_raw(i)
	//}
	//test_request()

	for {
		time.Sleep(time.Second * 5)
	}
}

func server(ip string) {
	if ip == "" {
		ip = "127.0.0.1"
	}
	var err error
	l, err := net.Listen("tcp", ip+":9999")
	if err != nil {
		log.Println(err)
	}
	go func() {
		conn1, err = l.Accept()
		if err != nil {
			log.Println(err)
		}
	}()
	return
}

func client(ip string) {
	if ip == "" {
		ip = "127.0.0.1"
	}
	var err error
	conn2, err = net.Dial("tcp", ip+":9999")
	if err != nil {
		log.Println(err)
	}
}

func test_request() {
	conn, _ := net.Dial("tcp", "127.0.0.1:7777")
	for i := 0; i < 1000; i++ {
		conn.Write([]byte(`GET / HTTP/1.1
Host: 127.0.0.1:7777
Connection: keep-alive


`))
		r, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			log.Println("close by read response err", err)
			break
		}
		log.Println("read response success", r)
		b, err := httputil.DumpResponse(r, true)
		if err != nil {
			log.Println("close by dump response err", err)
			break
		}
		fmt.Println(string(b[:20]), err)
		//time.Sleep(time.Second)
	}
	log.Println("finish")
}

func test_raw(k int) {
	for i := 0; i < 1000; i++ {
		ti := time.Now()
		conn, err := net.Dial("tcp", "127.0.0.1:7777")
		if err != nil {
			log.Println("conn dial err", err)
		}
		tid := time.Now()
		conn.Write([]byte(`GET /videojs5/video.js HTTP/1.1
Host: 127.0.0.1:7777


`))
		tiw := time.Now()
		buf := make([]byte, 3572)
		n, err := io.ReadFull(conn, buf)
		//n, err := conn.Read(buf)
		if err != nil {
			log.Println("close by read response err", err)
			break
		}
		log.Println(n, string(buf[:50]), "\n--------------\n", string(buf[n-50:n]))
		//time.Sleep(time.Second)
		err = conn.Close()
		if err != nil {
			log.Println("close conn err ", err)
		}
		now := time.Now()
		du := now.Sub(ti).Seconds()
		dud := now.Sub(tid).Seconds()
		duw := now.Sub(tiw).Seconds()
		if du > 1 {
			log.Println("duration long", du, dud, duw, k, i)
		}
		if n != 3572 {
			log.Println("n loss", n, string(buf))
		}
	}
	log.Println("finish")
}

func TestNewConn(t *testing.T) {
	buf := make([]byte, 1024)
	log.Println(len(buf), cap(buf))
	//b := pool.GetBufPoolCopy()
	//b[0] = 1
	//b[1] = 2
	//b[2] = 3
	b := []byte{1, 2, 3}
	log.Println(copy(buf[:3], b), len(buf), cap(buf))
	log.Println(len(buf), buf[0])
}

func TestDQueue(t *testing.T) {
	d := new(bufDequeue)
	d.vals = make([]unsafe.Pointer, 8)
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 10; i++ {
			log.Println(i)
			log.Println(d.popTail())
		}
	}()
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 10; i++ {
			data := "test"
			go log.Println(i, unsafe.Pointer(&data), d.pushHead(unsafe.Pointer(&data)))
		}
	}()
	time.Sleep(time.Second * 3)
}

func TestChain(t *testing.T) {
	go func() {
		log.Println(http.ListenAndServe("0.0.0.0:8889", nil))
	}()
	time.Sleep(time.Second * 5)
	d := new(bufChain)
	d.new(256)
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 30000; i++ {
			unsa, ok := d.popTail()
			str := (*string)(unsa)
			if ok {
				fmt.Println(i, str, *str, ok)
				//logs.Warn(i, str, *str, ok)
			} else {
				fmt.Println("nil", i, ok)
				//logs.Warn("nil", i, ok)
			}
		}
	}()
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 3000; i++ {
			go func(i int) {
				for n := 0; n < 10; n++ {
					data := "test " + strconv.Itoa(i) + strconv.Itoa(n)
					fmt.Println(data, unsafe.Pointer(&data))
					//logs.Warn(data, unsafe.Pointer(&data))
					d.pushHead(unsafe.Pointer(&data))
				}
			}(i)
		}
	}()
	time.Sleep(time.Second * 100000)
}

//func TestReceive(t *testing.T) {
//	go func() {
//		log.Println(http.ListenAndServe("0.0.0.0:8889", nil))
//	}()
//	logs.EnableFuncCallDepth(true)
//	logs.SetLogFuncCallDepth(3)
//	time.Sleep(time.Second * 5)
//	mux := New(Mux)
//	mux.bw.readBandwidth = float64(1*1024*1024)
//	mux.latency = float64(1/1000)
//	wind := New(receiveWindow)
//	wind.New(mux)
//	wind.
//	go func() {
//		time.Sleep(time.Second)
//		for i := 0; i < 36000; i++ {
//			data := d.Pop()
//			//fmt.Println(i, string(data.buf), err)
//			logs.Warn(i, string(data.content), data)
//		}
//	}()
//	go func() {
//		time.Sleep(time.Second*10)
//		for i := 0; i < 3000; i++ {
//			go func(i int) {
//				for n := 0; n < 10; n++{
//					data := New(common.muxPackager)
//					by := []byte("test " + strconv.Itoa(i) + strconv.Itoa(n))
//					_ = data.NewPac(common.MUX_NEW_MSG_PART, int32(i), by)
//					//fmt.Println(string((*data).buf), data)
//					logs.Warn(string((*data).content), data)
//					d.Push(data)
//				}
//			}(i)
//			go func(i int) {
//				data := New(common.muxPackager)
//				_ = data.NewPac(common.MUX_NEW_CONN, int32(i), nil)
//				//fmt.Println(string((*data).buf), data)
//				logs.Warn(data)
//				d.Push(data)
//			}(i)
//			go func(i int) {
//				data := New(common.muxPackager)
//				_ = data.NewPac(common.MUX_NEW_CONN_OK, int32(i), nil)
//				//fmt.Println(string((*data).buf), data)
//				logs.Warn(data)
//				d.Push(data)
//			}(i)
//		}
//	}()
//	time.Sleep(time.Second * 100000)
//}
