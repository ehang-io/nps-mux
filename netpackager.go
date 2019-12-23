package nps_mux

import (
	"encoding/binary"
	"errors"
	"io"
)

type BasePackager struct {
	Length  uint16
	Content []byte
}

func (Self *BasePackager) Set(content []byte) (err error) {
	Self.reset()
	if content != nil {
		n := len(content)
		if n == 0 {
			err = errors.New("mux:packer: newpack content is zero length")
		}
		if n > MaximumSegmentSize {
			err = errors.New("mux:packer: newpack content segment too large")
			return
		}
		Self.Content = Self.Content[:n]
		copy(Self.Content, content)
	} else {
		err = errors.New("mux:packer: newpack content is nil")
	}
	Self.setLength()
	return
}

func (Self *BasePackager) Pack(writer io.Writer) (err error) {
	err = binary.Write(writer, binary.LittleEndian, Self.Length)
	if err != nil {
		return
	}
	err = binary.Write(writer, binary.LittleEndian, Self.Content)
	return
}

func (Self *BasePackager) UnPack(reader io.Reader) (n uint16, err error) {
	Self.reset()
	n += 2 // uint16
	err = binary.Read(reader, binary.LittleEndian, &Self.Length)
	if err != nil {
		return
	}
	if int(Self.Length) > cap(Self.Content) {
		err = errors.New("mux:packer: unpack err, content length too large")
		return
	}
	if Self.Length > MaximumSegmentSize {
		err = errors.New("mux:packer: unpack content segment too large")
		return
	}
	Self.Content = Self.Content[:int(Self.Length)]
	err = binary.Read(reader, binary.LittleEndian, Self.Content)
	n += Self.Length
	return
}

func (Self *BasePackager) setLength() {
	Self.Length = uint16(len(Self.Content))
	return
}

func (Self *BasePackager) reset() {
	Self.Length = 0
	Self.Content = Self.Content[:0] // reset length
}

type MuxPackager struct {
	Flag         uint8
	Id           int32
	RemainLength uint32
	BasePackager
}

func (Self *MuxPackager) Set(flag uint8, id int32, content interface{}) (err error) {
	Self.Flag = flag
	Self.Id = id
	switch flag {
	case MuxPingFlag, MuxPingReturn, MuxNewMsg, MuxNewMsgPart:
		Self.Content = WindowBuff.Get()
		err = Self.BasePackager.Set(content.([]byte))
		//logs.Warn(Self.Length, string(Self.Content))
	case MuxMsgSendOk:
		// MUX_MSG_SEND_OK contains uint32 data
		Self.RemainLength = content.(uint32)
	}
	return
}

func (Self *MuxPackager) Pack(writer io.Writer) (err error) {
	err = binary.Write(writer, binary.LittleEndian, Self.Flag)
	if err != nil {
		return
	}
	err = binary.Write(writer, binary.LittleEndian, Self.Id)
	if err != nil {
		return
	}
	switch Self.Flag {
	case MuxNewMsg, MuxNewMsgPart, MuxPingFlag, MuxPingReturn:
		err = Self.BasePackager.Pack(writer)
		WindowBuff.Put(Self.Content)
	case MuxMsgSendOk:
		err = binary.Write(writer, binary.LittleEndian, Self.RemainLength)
	}
	return
}

func (Self *MuxPackager) UnPack(reader io.Reader) (n uint16, err error) {
	err = binary.Read(reader, binary.LittleEndian, &Self.Flag)
	if err != nil {
		return
	}
	err = binary.Read(reader, binary.LittleEndian, &Self.Id)
	if err != nil {
		return
	}
	switch Self.Flag {
	case MuxNewMsg, MuxNewMsgPart, MuxPingFlag, MuxPingReturn:
		Self.Content = WindowBuff.Get() // need get a window buf from pool
		n, err = Self.BasePackager.UnPack(reader)
	case MuxMsgSendOk:
		err = binary.Read(reader, binary.LittleEndian, &Self.RemainLength)
		n += 4 // uint32
	}
	n += 5 //uint8 int32
	return
}

func (Self *MuxPackager) Reset() {
	Self.Id = 0
	Self.Flag = 0
	Self.Length = 0
	Self.Content = nil
	Self.RemainLength = 0
}
