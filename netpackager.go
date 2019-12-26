package nps_mux

import (
	"encoding/binary"
	"errors"
	"io"
)

type basePackager struct {
	length  uint16
	content []byte
}

func (Self *basePackager) Set(content []byte) (err error) {
	Self.reset()
	if content != nil {
		n := len(content)
		if n == 0 {
			err = errors.New("mux:packer: newpack content is zero length")
		}
		if n > maximumSegmentSize {
			err = errors.New("mux:packer: newpack content segment too large")
			return
		}
		Self.content = Self.content[:n]
		copy(Self.content, content)
	} else {
		err = errors.New("mux:packer: newpack content is nil")
	}
	Self.setLength()
	return
}

func (Self *basePackager) Pack(writer io.Writer) (err error) {
	err = binary.Write(writer, binary.LittleEndian, Self.length)
	if err != nil {
		return
	}
	err = binary.Write(writer, binary.LittleEndian, Self.content)
	return
}

func (Self *basePackager) UnPack(reader io.Reader) (n uint16, err error) {
	Self.reset()
	n += 2 // uint16
	err = binary.Read(reader, binary.LittleEndian, &Self.length)
	if err != nil {
		return
	}
	if int(Self.length) > cap(Self.content) {
		err = errors.New("mux:packer: unpack err, content length too large")
		return
	}
	if Self.length > maximumSegmentSize {
		err = errors.New("mux:packer: unpack content segment too large")
		return
	}
	Self.content = Self.content[:int(Self.length)]
	err = binary.Read(reader, binary.LittleEndian, Self.content)
	n += Self.length
	return
}

func (Self *basePackager) setLength() {
	Self.length = uint16(len(Self.content))
	return
}

func (Self *basePackager) reset() {
	Self.length = 0
	Self.content = Self.content[:0] // reset length
}

type muxPackager struct {
	flag         uint8
	id           int32
	remainLength uint32
	basePackager
}

func (Self *muxPackager) Set(flag uint8, id int32, content interface{}) (err error) {
	Self.flag = flag
	Self.id = id
	switch flag {
	case muxPingFlag, muxPingReturn, muxNewMsg, muxNewMsgPart:
		Self.content = windowBuff.Get()
		err = Self.basePackager.Set(content.([]byte))
	case muxMsgSendOk:
		// MUX_MSG_SEND_OK contains uint32 data
		Self.remainLength = content.(uint32)
	}
	return
}

func (Self *muxPackager) Pack(writer io.Writer) (err error) {
	err = binary.Write(writer, binary.LittleEndian, Self.flag)
	if err != nil {
		return
	}
	err = binary.Write(writer, binary.LittleEndian, Self.id)
	if err != nil {
		return
	}
	switch Self.flag {
	case muxNewMsg, muxNewMsgPart, muxPingFlag, muxPingReturn:
		err = Self.basePackager.Pack(writer)
		windowBuff.Put(Self.content)
	case muxMsgSendOk:
		err = binary.Write(writer, binary.LittleEndian, Self.remainLength)
	}
	return
}

func (Self *muxPackager) UnPack(reader io.Reader) (n uint16, err error) {
	err = binary.Read(reader, binary.LittleEndian, &Self.flag)
	if err != nil {
		return
	}
	err = binary.Read(reader, binary.LittleEndian, &Self.id)
	if err != nil {
		return
	}
	switch Self.flag {
	case muxNewMsg, muxNewMsgPart, muxPingFlag, muxPingReturn:
		Self.content = windowBuff.Get() // need Get a window buf from pool
		n, err = Self.basePackager.UnPack(reader)
	case muxMsgSendOk:
		err = binary.Read(reader, binary.LittleEndian, &Self.remainLength)
		n += 4 // uint32
	}
	n += 5 //uint8 int32
	return
}

func (Self *muxPackager) reset() {
	Self.id = 0
	Self.flag = 0
	Self.length = 0
	Self.content = nil
	Self.remainLength = 0
}
