package nps_mux

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/cnlh/nps/lib/common"
	"github.com/cnlh/nps/lib/goroutine"
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

	"github.com/astaxie/beego/logs"
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

func writeResult(vals []string, outfile string) error {
	file, err := os.Create(outfile)
	if err != nil {
		fmt.Println("writer", err)
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, v := range vals {
		writer.WriteString(v)
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
				time.Sleep(time.Second * 10)
				writeResult([]string{
					strconv.Itoa(int(mux.bw.Get() / 1024 / 1024)),
					strconv.Itoa(int(math.Float64frombits(atomic.LoadUint64(&mux.latency)))),
				}, serverResultFileName)
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
				writeResult([]string{
					strconv.Itoa(int(mux.bw.Get() / 1024 / 1024)),
					strconv.Itoa(int(math.Float64frombits(atomic.LoadUint64(&mux.latency)))),
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
			b := bytes.Repeat([]byte{0,}, 1024)
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
			err := writeResult([]string{strconv.Itoa(int(writeBw)), strconv.Itoa(int(readBw)),}, appResultFileName)
			if err != nil {
				t.Fatal(err)
			}
		}(userConn)
	}
}
func TestUser(t *testing.T) {
	appConn, err := net.Dial("tcp", serverIp+":"+serverPort)
	if err != nil {
		t.Fatal(err)
	}
	b := bytes.Repeat([]byte{0,}, 1024)
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
	err = writeResult([]string{strconv.Itoa(int(readBw)), strconv.Itoa(int(writeBw)),}, userResultFileName)
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
		logs.EnableFuncCallDepth(true)
		logs.SetLogFuncCallDepth(3)
		server(tc.Eth.EthAddr)
		client(tc.Eth.EthAddr)
		//poolConnCopy, _ := ants.NewPoolWithFunc(200000, common.copyConn, ants.WithNonblocking(false))
		time.Sleep(time.Second * 3)
		rate := NewRate(1024 * 1024 * 3)
		rate.Start()
		conn2 = NewRateConn(rate, conn2)
		go func() {
			m2 := NewMux(conn2, "tcp")
			for {
				c, err := m2.Accept()
				if err != nil {
					logs.Warn(err)
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
			logs.Warn("nps new conn err ", err)
			return
		}
		buf := make([]byte, 1024*1024)
		var count float64
		count = 0
		start := time.Now()
		for {
			n, err := tmpCpnn.Read(buf)
			count += float64(n)
			logs.Warn(m1.bw.Get())
			logs.Warn(uint32(math.Float64frombits(atomic.LoadUint64(&m1.latency))))
			if err != nil {
				logs.Warn(err)
				break
			}
		}
		logs.Warn("now rate", count/time.Now().Sub(start).Seconds()/1024/1024)
	})
	logs.Warning(err.Error())
}
func TestNewMux(t *testing.T) {
	go func() {
		http.ListenAndServe("0.0.0.0:8889", nil)
	}()
	logs.EnableFuncCallDepth(true)
	logs.SetLogFuncCallDepth(3)
	server("")
	client("")
	//poolConnCopy, _ := ants.NewPoolWithFunc(200000, common.copyConn, ants.WithNonblocking(false))
	time.Sleep(time.Second * 3)
	go func() {
		m2 := NewMux(conn2, "tcp")
		for {
			//logs.Warn("npc starting accept")
			c, err := m2.Accept()
			if err != nil {
				logs.Warn(err)
				continue
			}
			//logs.Warn("npc accept success ")
			c2, err := net.Dial("tcp", "127.0.0.1:80")
			if err != nil {
				logs.Warn(err)
				c.Close()
				continue
			}
			//c2.(*net.TCPConn).SetReadBuffer(0)
			//c2.(*net.TCPConn).SetReadBuffer(0)
			_ = goroutine.CopyConnsPool.Invoke(goroutine.NewConns(c, c2, nil))
			//go func(c2 net.Conn, c *conn) {
			//	wg := New(sync.WaitGroup)
			//	wg.Add(2)
			//	_ = poolConnCopy.Invoke(common.newConnGroup(c2, c, wg))
			//	//go func() {
			//	//	_, err = common.CopyBuffer(c2, c)
			//	//	if err != nil {
			//	//		c2.Close()
			//	//		c.Close()
			//	//		//logs.Warn("close npc by copy from nps", err, c.connId)
			//	//	}
			//	//	wg.Done()
			//	//}()
			//	//wg.Add(1)
			//	_ = poolConnCopy.Invoke(common.newConnGroup(c, c2, wg))
			//	//go func() {
			//	//	_, err = common.CopyBuffer(c, c2)
			//	//	if err != nil {
			//	//		c2.Close()
			//	//		c.Close()
			//	//		//logs.Warn("close npc by copy from server", err, c.connId)
			//	//	}
			//	//	wg.Done()
			//	//}()
			//	//logs.Warn("npc wait")
			//	wg.Wait()
			//}(c2, c.(*conn))
		}
	}()

	go func() {
		m1 := NewMux(conn1, "tcp")
		l, err := net.Listen("tcp", "127.0.0.1:7777")
		if err != nil {
			logs.Warn(err)
		}
		for {
			//logs.Warn("nps starting accept")
			conns, err := l.Accept()
			if err != nil {
				logs.Warn(err)
				continue
			}
			//conns.(*net.TCPConn).SetReadBuffer(0)
			//conns.(*net.TCPConn).SetReadBuffer(0)
			//logs.Warn("nps accept success starting New conn")
			tmpCpnn, err := m1.NewConn()
			if err != nil {
				logs.Warn("nps New conn err ", err)
				continue
			}
			//logs.Warn("nps New conn success ", tmpCpnn.connId)
			_ = goroutine.CopyConnsPool.Invoke(goroutine.NewConns(tmpCpnn, conns, nil))
			//go func(tmpCpnn *conn, conns net.Conn) {
			//	wg := New(sync.WaitGroup)
			//	wg.Add(2)
			//	_ = poolConnCopy.Invoke(common.newConnGroup(tmpCpnn, conns, wg))
			//	//go func() {
			//	//	_, err := common.CopyBuffer(tmpCpnn, conns)
			//	//	if err != nil {
			//	//		conns.Close()
			//	//		tmpCpnn.Close()
			//	//		//logs.Warn("close nps by copy from user", tmpCpnn.connId, err)
			//	//	}
			//	//}()
			//	//wg.Add(1)
			//	_ = poolConnCopy.Invoke(common.newConnGroup(conns, tmpCpnn, wg))
			//	//time.Sleep(time.Second)
			//	//_, err = common.CopyBuffer(conns, tmpCpnn)
			//	//if err != nil {
			//	//	conns.Close()
			//	//	tmpCpnn.Close()
			//	//	//logs.Warn("close nps by copy from npc ", tmpCpnn.connId, err)
			//	//}
			//	wg.Wait()
			//}(tmpCpnn, conns)
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
		logs.Warn(err)
	}
	go func() {
		conn1, err = l.Accept()
		if err != nil {
			logs.Warn(err)
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
		logs.Warn(err)
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
			logs.Warn("close by read response err", err)
			break
		}
		logs.Warn("read response success", r)
		b, err := httputil.DumpResponse(r, true)
		if err != nil {
			logs.Warn("close by dump response err", err)
			break
		}
		fmt.Println(string(b[:20]), err)
		//time.Sleep(time.Second)
	}
	logs.Warn("finish")
}

func test_raw(k int) {
	for i := 0; i < 1000; i++ {
		ti := time.Now()
		conn, err := net.Dial("tcp", "127.0.0.1:7777")
		if err != nil {
			logs.Warn("conn dial err", err)
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
			logs.Warn("close by read response err", err)
			break
		}
		logs.Warn(n, string(buf[:50]), "\n--------------\n", string(buf[n-50:n]))
		//time.Sleep(time.Second)
		err = conn.Close()
		if err != nil {
			logs.Warn("close conn err ", err)
		}
		now := time.Now()
		du := now.Sub(ti).Seconds()
		dud := now.Sub(tid).Seconds()
		duw := now.Sub(tiw).Seconds()
		if du > 1 {
			logs.Warn("duration long", du, dud, duw, k, i)
		}
		if n != 3572 {
			logs.Warn("n loss", n, string(buf))
		}
	}
	logs.Warn("finish")
}

func TestNewConn(t *testing.T) {
	buf := common.GetBufPoolCopy()
	logs.Warn(len(buf), cap(buf))
	//b := pool.GetBufPoolCopy()
	//b[0] = 1
	//b[1] = 2
	//b[2] = 3
	b := []byte{1, 2, 3}
	logs.Warn(copy(buf[:3], b), len(buf), cap(buf))
	logs.Warn(len(buf), buf[0])
}

func TestDQueue(t *testing.T) {
	logs.EnableFuncCallDepth(true)
	logs.SetLogFuncCallDepth(3)
	d := new(bufDequeue)
	d.vals = make([]unsafe.Pointer, 8)
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 10; i++ {
			logs.Warn(i)
			logs.Warn(d.popTail())
		}
	}()
	go func() {
		time.Sleep(time.Second)
		for i := 0; i < 10; i++ {
			data := "test"
			go logs.Warn(i, unsafe.Pointer(&data), d.pushHead(unsafe.Pointer(&data)))
		}
	}()
	time.Sleep(time.Second * 3)
}

func TestChain(t *testing.T) {
	go func() {
		log.Println(http.ListenAndServe("0.0.0.0:8889", nil))
	}()
	logs.EnableFuncCallDepth(true)
	logs.SetLogFuncCallDepth(3)
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
