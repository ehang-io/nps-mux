package nps_mux

import (
	"sync"
)

const (
	PoolSizeBuffer = 4096                           // a mux packager total length
	PoolSizeWindow = PoolSizeBuffer - 2 - 4 - 4 - 1 // content length
)

type windowBufferPool struct {
	pool sync.Pool
}

func (Self *windowBufferPool) New() {
	Self.pool = sync.Pool{
		New: func() interface{} {
			return make([]byte, PoolSizeWindow, PoolSizeWindow)
		},
	}
}

func (Self *windowBufferPool) Get() (buf []byte) {
	buf = Self.pool.Get().([]byte)
	return buf[:PoolSizeWindow]
}

func (Self *windowBufferPool) Put(x []byte) {
	Self.pool.Put(x[:PoolSizeWindow]) // make buf to full
}

//type bufferPool struct {
//	pool sync.Pool
//}

//func (Self *bufferPool) New() {
//	Self.pool = sync.Pool{
//		New: func() interface{} {
//			return bytes.NewBuffer(make([]byte, 0, PoolSizeBuffer))
//		},
//	}
//}
//
//func (Self *bufferPool) Get() *bytes.Buffer {
//	return Self.pool.Get().(*bytes.Buffer)
//}
//
//func (Self *bufferPool) Put(x *bytes.Buffer) {
//	x.Reset()
//	Self.pool.Put(x)
//}

type muxPackagerPool struct {
	pool sync.Pool
}

func (Self *muxPackagerPool) New() {
	Self.pool = sync.Pool{
		New: func() interface{} {
			pack := MuxPackager{}
			return &pack
		},
	}
}

func (Self *muxPackagerPool) Get() *MuxPackager {
	return Self.pool.Get().(*MuxPackager)
}

func (Self *muxPackagerPool) Put(pack *MuxPackager) {
	pack.Reset()
	Self.pool.Put(pack)
}

type listElementPool struct {
	pool sync.Pool
}

func (Self *listElementPool) New() {
	Self.pool = sync.Pool{
		New: func() interface{} {
			element := ListElement{}
			return &element
		},
	}
}

func (Self *listElementPool) Get() *ListElement {
	return Self.pool.Get().(*ListElement)
}

func (Self *listElementPool) Put(element *ListElement) {
	element.Reset()
	Self.pool.Put(element)
}

var once = sync.Once{}
var MuxPack = muxPackagerPool{}
var WindowBuff = windowBufferPool{}
var ListElementPool = listElementPool{}

func newPool() {
	MuxPack.New()
	WindowBuff.New()
	ListElementPool.New()
}

func init() {
	once.Do(newPool)
}
