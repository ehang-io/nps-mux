package nps_mux

import (
	"errors"
	"io"
	"math"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type conn struct {
	net.Conn
	connStatusOkCh   chan struct{}
	connStatusFailCh chan struct{}
	connId           int32
	isClose          bool
	closingFlag      bool // closing conn flag
	receiveWindow    *receiveWindow
	sendWindow       *sendWindow
	once             sync.Once
}

func NewConn(connId int32, mux *Mux) *conn {
	c := &conn{
		connStatusOkCh:   make(chan struct{}),
		connStatusFailCh: make(chan struct{}),
		connId:           connId,
		receiveWindow:    new(receiveWindow),
		sendWindow:       new(sendWindow),
		once:             sync.Once{},
	}
	c.receiveWindow.New(mux)
	c.sendWindow.New(mux)
	return c
}

func (s *conn) Read(buf []byte) (n int, err error) {
	if s.isClose || buf == nil {
		return 0, errors.New("the conn has closed")
	}
	if len(buf) == 0 {
		return 0, nil
	}
	// waiting for takeout from receive window finish or timeout
	n, err = s.receiveWindow.Read(buf, s.connId)
	return
}

func (s *conn) Write(buf []byte) (n int, err error) {
	if s.isClose {
		return 0, errors.New("the conn has closed")
	}
	if s.closingFlag {
		return 0, errors.New("io: write on closed conn")
	}
	if len(buf) == 0 {
		return 0, nil
	}
	n, err = s.sendWindow.WriteFull(buf, s.connId)
	return
}

func (s *conn) Close() (err error) {
	s.once.Do(s.closeProcess)
	return
}

func (s *conn) closeProcess() {
	s.isClose = true
	s.receiveWindow.mux.connMap.Delete(s.connId)
	if !s.receiveWindow.mux.IsClose {
		// if server or user close the conn while reading, will Get a io.EOF
		// and this Close method will be invoke, send this signal to close other side
		s.receiveWindow.mux.sendInfo(muxConnClose, s.connId, nil)
	}
	s.sendWindow.CloseWindow()
	s.receiveWindow.CloseWindow()
	return
}

func (s *conn) LocalAddr() net.Addr {
	return s.receiveWindow.mux.conn.LocalAddr()
}

func (s *conn) RemoteAddr() net.Addr {
	return s.receiveWindow.mux.conn.RemoteAddr()
}

func (s *conn) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	_ = s.SetWriteDeadline(t)
	return nil
}

func (s *conn) SetReadDeadline(t time.Time) error {
	s.receiveWindow.SetTimeOut(t)
	return nil
}

func (s *conn) SetWriteDeadline(t time.Time) error {
	s.sendWindow.SetTimeOut(t)
	return nil
}

type window struct {
	remainingWait uint64 // 64bit alignment
	off           uint32
	maxSize       uint32
	closeOp       bool
	closeOpCh     chan struct{}
	mux           *Mux
}

func (Self *window) unpack(ptrs uint64) (remaining, wait uint32) {
	const mask = 1<<dequeueBits - 1
	remaining = uint32((ptrs >> dequeueBits) & mask)
	wait = uint32(ptrs & mask)
	return
}

func (Self *window) pack(remaining, wait uint32) uint64 {
	const mask = 1<<dequeueBits - 1
	return (uint64(remaining) << dequeueBits) |
		uint64(wait&mask)
}

func (Self *window) New() {
	Self.closeOpCh = make(chan struct{}, 2)
}

func (Self *window) CloseWindow() {
	if !Self.closeOp {
		Self.closeOp = true
		Self.closeOpCh <- struct{}{}
		Self.closeOpCh <- struct{}{}
	}
}

type receiveWindow struct {
	window
	bufQueue receiveWindowQueue
	element  *listElement
	count    int8
	once     sync.Once
}

func (Self *receiveWindow) New(mux *Mux) {
	// initial a window for receive
	Self.bufQueue.New()
	Self.element = listEle.Get()
	Self.maxSize = maximumSegmentSize * 10
	Self.mux = mux
	Self.window.New()
}

func (Self *receiveWindow) remainingSize(delta uint16) (n uint32) {
	// receive window remaining
	l := int64(atomic.LoadUint32(&Self.maxSize)) - int64(Self.bufQueue.Len())
	l -= int64(delta)
	if l > 0 {
		n = uint32(l)
	}
	return
}

func (Self *receiveWindow) calcSize() {
	// calculating maximum receive window size
	if Self.count == 0 {
		conns := Self.mux.connMap.Size()
		n := uint32(math.Float64frombits(atomic.LoadUint64(&Self.mux.latency)) *
			Self.mux.bw.Get() / float64(conns))
		if n < maximumSegmentSize*10 {
			n = maximumSegmentSize * 10
		}
		bufLen := Self.bufQueue.Len()
		if n < bufLen {
			n = bufLen
		}
		if n < Self.maxSize/2 {
			n = Self.maxSize / 2
		}
		// Set the minimal size
		if n > 2*Self.maxSize {
			n = 2 * Self.maxSize
		}
		if n > (maximumWindowSize / uint32(conns)) {
			n = maximumWindowSize / uint32(conns)
		}
		// Set the maximum size
		atomic.StoreUint32(&Self.maxSize, n)
		Self.count = -10
	}
	Self.count += 1
	return
}

func (Self *receiveWindow) Write(buf []byte, l uint16, part bool, id int32) (err error) {
	if Self.closeOp {
		return errors.New("conn.receiveWindow: write on closed window")
	}
	element, err := newListElement(buf, l, part)
	if err != nil {
		return
	}
	Self.calcSize() // calculate the max window size
	var wait uint32
start:
	ptrs := atomic.LoadUint64(&Self.remainingWait)
	_, wait = Self.unpack(ptrs)
	newRemaining := Self.remainingSize(l)
	// calculate the remaining window size now, plus the element we will push
	if newRemaining == 0 {
		wait = 1
	}
	if !atomic.CompareAndSwapUint64(&Self.remainingWait, ptrs, Self.pack(0, wait)) {
		goto start
		// another goroutine change the status, make sure shall we need wait
	}
	Self.bufQueue.Push(element)
	// status check finish, now we can push the element into the queue
	if wait == 0 {
		Self.mux.sendInfo(muxMsgSendOk, id, newRemaining)
		// send the remaining window size, not including zero size
	}
	return nil
}

func (Self *receiveWindow) Read(p []byte, id int32) (n int, err error) {
	if Self.closeOp {
		return 0, io.EOF // receive close signal, returns eof
	}
	pOff := 0
	l := 0
copyData:
	if Self.off == uint32(Self.element.L) {
		// on the first Read method invoked, Self.off and Self.element.l
		// both zero value
		listEle.Put(Self.element)
		if Self.closeOp {
			return 0, io.EOF
		}
		Self.element, err = Self.bufQueue.Pop()
		// if the queue is empty, Pop method will wait until one element push
		// into the queue successful, or timeout.
		// timer start on timeout parameter is Set up
		Self.off = 0
		if err != nil {
			Self.CloseWindow() // also close the window, to avoid read twice
			return             // queue receive stop or time out, break the loop and return
		}
	}
	l = copy(p[pOff:], Self.element.Buf[Self.off:Self.element.L])
	pOff += l
	Self.off += uint32(l)
	n += l
	l = 0
	if Self.off == uint32(Self.element.L) {
		windowBuff.Put(Self.element.Buf)
		Self.sendStatus(id, Self.element.L)
		// check the window full status
	}
	if pOff < len(p) && Self.element.Part {
		// element is a part of the segments, trying to fill up buf p
		goto copyData
	}
	return // buf p is full or all of segments in buf, return
}

func (Self *receiveWindow) sendStatus(id int32, l uint16) {
	var remaining, wait uint32
	for {
		ptrs := atomic.LoadUint64(&Self.remainingWait)
		remaining, wait = Self.unpack(ptrs)
		remaining += uint32(l)
		if atomic.CompareAndSwapUint64(&Self.remainingWait, ptrs, Self.pack(remaining, 0)) {
			break
		}
		runtime.Gosched()
		// another goroutine change remaining or wait status, make sure
		// we need acknowledge other side
	}
	// now we Get the current window status success
	if wait == 1 {
		Self.mux.sendInfo(muxMsgSendOk, id, remaining)
	}
	return
}

func (Self *receiveWindow) SetTimeOut(t time.Time) {
	// waiting for FIFO queue Pop method
	Self.bufQueue.SetTimeOut(t)
}

func (Self *receiveWindow) Stop() {
	// queue has no more data to push, so unblock pop method
	Self.once.Do(Self.bufQueue.Stop)
}

func (Self *receiveWindow) CloseWindow() {
	Self.window.CloseWindow()
	Self.Stop()
	Self.release()
}

func (Self *receiveWindow) release() {
	for {
		ele := Self.bufQueue.TryPop()
		if ele == nil {
			break
		}
		if ele.Buf != nil {
			windowBuff.Put(ele.Buf)
		}
		listEle.Put(ele)
	} // release resource
}

type sendWindow struct {
	window
	buf       []byte
	setSizeCh chan struct{}
	timeout   time.Time
}

func (Self *sendWindow) New(mux *Mux) {
	Self.setSizeCh = make(chan struct{})
	Self.maxSize = maximumSegmentSize * 10
	atomic.AddUint64(&Self.remainingWait, uint64(maximumSegmentSize*10)<<dequeueBits)
	Self.mux = mux
	Self.window.New()
}

func (Self *sendWindow) SetSendBuf(buf []byte) {
	// send window buff from conn write method, Set it to send window
	Self.buf = buf
	Self.off = 0
}

func (Self *sendWindow) SetSize(newRemaining uint32) (closed bool) {
	// Set the window size from receive window
	defer func() {
		if recover() != nil {
			closed = true
		}
	}()
	if Self.closeOp {
		close(Self.setSizeCh)
		return true
	}
	var remaining, wait, newWait uint32
	for {
		ptrs := atomic.LoadUint64(&Self.remainingWait)
		remaining, wait = Self.unpack(ptrs)
		if remaining == newRemaining {
			//logs.Warn("waiting for another window size")
			return false // waiting for receive another usable window size
		}
		if newRemaining == 0 && wait == 1 {
			newWait = 1 // keep the wait status,
			// also if newRemaining is not zero, change wait to 0
		}
		if atomic.CompareAndSwapUint64(&Self.remainingWait, ptrs, Self.pack(newRemaining, newWait)) {
			break
		}
		// anther goroutine change wait status or window size
	}
	if wait == 1 {
		// send window into the wait status, need notice the channel
		Self.allow()
	}
	// send window not into the wait status, so just do slide
	return false
}

func (Self *sendWindow) allow() {
	select {
	case Self.setSizeCh <- struct{}{}:
		return
	case <-Self.closeOpCh:
		close(Self.setSizeCh)
		return
	}
}

func (Self *sendWindow) sent(sentSize uint32) {
	atomic.AddUint64(&Self.remainingWait, ^(uint64(sentSize)<<dequeueBits - 1))
}

func (Self *sendWindow) WriteTo() (p []byte, sendSize uint32, part bool, err error) {
	// returns buf segments, return only one segments, need a loop outside
	// until err = io.EOF
	if Self.closeOp {
		return nil, 0, false, errors.New("conn.writeWindow: window closed")
	}
	if Self.off == uint32(len(Self.buf)) {
		return nil, 0, false, io.EOF
		// send window buff is drain, return eof and Get another one
	}
	var remaining uint32
start:
	ptrs := atomic.LoadUint64(&Self.remainingWait)
	remaining, _ = Self.unpack(ptrs)
	if remaining == 0 {
		if !atomic.CompareAndSwapUint64(&Self.remainingWait, ptrs, Self.pack(0, 1)) {
			goto start // another goroutine change the window, try again
		}
		// into the wait status
		err = Self.waitReceiveWindow()
		if err != nil {
			return nil, 0, false, err
		}
		goto start
	}
	// there are still remaining window
	if len(Self.buf[Self.off:]) > maximumSegmentSize {
		sendSize = maximumSegmentSize
	} else {
		sendSize = uint32(len(Self.buf[Self.off:]))
	}
	if remaining < sendSize {
		// usable window size is small than
		// window MAXIMUM_SEGMENT_SIZE or send buf left
		sendSize = remaining
	}
	if sendSize < uint32(len(Self.buf[Self.off:])) {
		part = true
	}
	p = Self.buf[Self.off : sendSize+Self.off]
	Self.off += sendSize
	Self.sent(sendSize)
	return
}

func (Self *sendWindow) waitReceiveWindow() (err error) {
	t := Self.timeout.Sub(time.Now())
	if t < 0 { // not Set the timeout, wait for it as long as connection close
		select {
		case _, ok := <-Self.setSizeCh:
			if !ok {
				return errors.New("conn.writeWindow: window closed")
			}
			return nil
		case <-Self.closeOpCh:
			return errors.New("conn.writeWindow: window closed")
		}
	}
	timer := time.NewTimer(t)
	defer timer.Stop()
	// waiting for receive usable window size, or timeout
	select {
	case _, ok := <-Self.setSizeCh:
		if !ok {
			return errors.New("conn.writeWindow: window closed")
		}
		return nil
	case <-timer.C:
		return errors.New("conn.writeWindow: write to time out")
	case <-Self.closeOpCh:
		return errors.New("conn.writeWindow: window closed")
	}
}

func (Self *sendWindow) WriteFull(buf []byte, id int32) (n int, err error) {
	Self.SetSendBuf(buf) // Set the buf to send window
	var bufSeg []byte
	var part bool
	var l uint32
	for {
		bufSeg, l, part, err = Self.WriteTo()
		// Get the buf segments from send window
		if bufSeg == nil && part == false && err == io.EOF {
			// send window is drain, break the loop
			err = nil
			break
		}
		if err != nil {
			break
		}
		n += int(l)
		l = 0
		if part {
			Self.mux.sendInfo(muxNewMsgPart, id, bufSeg)
		} else {
			Self.mux.sendInfo(muxNewMsg, id, bufSeg)
		}
		// send to other side, not send nil data to other side
	}
	return
}

func (Self *sendWindow) SetTimeOut(t time.Time) {
	// waiting for receive a receive window size
	Self.timeout = t
}
