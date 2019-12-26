package nps_mux

import (
	"sync"
)

const (
	poolSizeBuffer = 4096                           // a mux packager total length
	poolSizeWindow = poolSizeBuffer - 2 - 4 - 4 - 1 // content length
)

type windowBufferPool struct {
	pool sync.Pool
}

func newWindowBufferPool() *windowBufferPool {
	return &windowBufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, poolSizeWindow, poolSizeWindow)
			},
		},
	}
}

func (Self *windowBufferPool) Get() (buf []byte) {
	buf = Self.pool.Get().([]byte)
	return buf[:poolSizeWindow]
}

func (Self *windowBufferPool) Put(x []byte) {
	Self.pool.Put(x[:poolSizeWindow]) // make buf to full
}

type muxPackagerPool struct {
	pool sync.Pool
}

func newMuxPackagerPool() *muxPackagerPool {
	return &muxPackagerPool{
		pool: sync.Pool{
			New: func() interface{} {
				pack := muxPackager{}
				return &pack
			},
		},
	}
}

func (Self *muxPackagerPool) Get() *muxPackager {
	return Self.pool.Get().(*muxPackager)
}

func (Self *muxPackagerPool) Put(pack *muxPackager) {
	pack.reset()
	Self.pool.Put(pack)
}

type listElementPool struct {
	pool sync.Pool
}

func newListElementPool() *listElementPool {
	return &listElementPool{
		pool: sync.Pool{
			New: func() interface{} {
				element := listElement{}
				return &element
			},
		},
	}
}

func (Self *listElementPool) Get() *listElement {
	return Self.pool.Get().(*listElement)
}

func (Self *listElementPool) Put(element *listElement) {
	element.Reset()
	Self.pool.Put(element)
}

var (
	muxPack    = newMuxPackagerPool()
	windowBuff = newWindowBufferPool()
	listEle    = newListElementPool()
)
