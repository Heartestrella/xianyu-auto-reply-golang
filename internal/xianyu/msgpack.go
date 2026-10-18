package xianyu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// decodeMessagePack decodes the subset of MessagePack used by Goofish sync
// pushes. Map keys are normalized to strings because the protocol uses numeric
// field identifiers ("1", "2", "6", "10").
func decodeMessagePack(data []byte) (any, error) {
	d := messagePackDecoder{data: data}
	value, err := d.value()
	if err != nil {
		return nil, err
	}
	return value, nil
}

type messagePackDecoder struct {
	data []byte
	pos  int
}

func (d *messagePackDecoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.data) {
		return nil, errors.New("messagepack数据不完整")
	}
	b := d.data[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *messagePackDecoder) uint(n int) (uint64, error) {
	b, err := d.take(n)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return uint64(b[0]), nil
	case 2:
		return uint64(binary.BigEndian.Uint16(b)), nil
	case 4:
		return uint64(binary.BigEndian.Uint32(b)), nil
	case 8:
		return binary.BigEndian.Uint64(b), nil
	}
	return 0, errors.New("无效整数长度")
}

func (d *messagePackDecoder) value() (any, error) {
	b, err := d.take(1)
	if err != nil {
		return nil, err
	}
	c := b[0]
	switch {
	case c <= 0x7f:
		return uint64(c), nil
	case c >= 0xe0:
		return int64(int8(c)), nil
	case c&0xe0 == 0xa0:
		return d.string(int(c & 0x1f))
	case c&0xf0 == 0x90:
		return d.array(int(c & 0x0f))
	case c&0xf0 == 0x80:
		return d.object(int(c & 0x0f))
	}
	switch c {
	case 0xc0:
		return nil, nil
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xc4, 0xc5, 0xc6:
		n, e := d.length(c, 0xc4)
		if e != nil {
			return nil, e
		}
		return d.take(n)
	case 0xca:
		n, e := d.uint(4)
		return float64(math.Float32frombits(uint32(n))), e
	case 0xcb:
		n, e := d.uint(8)
		return math.Float64frombits(n), e
	case 0xcc, 0xcd, 0xce, 0xcf:
		return d.uint(1 << (c - 0xcc))
	case 0xd0, 0xd1, 0xd2, 0xd3:
		n := 1 << (c - 0xd0)
		u, e := d.uint(n)
		if e != nil {
			return nil, e
		}
		switch n {
		case 1:
			return int64(int8(u)), nil
		case 2:
			return int64(int16(u)), nil
		case 4:
			return int64(int32(u)), nil
		default:
			return int64(u), nil
		}
	case 0xd9, 0xda, 0xdb:
		n, e := d.length(c, 0xd9)
		if e != nil {
			return nil, e
		}
		return d.string(n)
	case 0xdc, 0xdd:
		n64, e := d.uint(2 << (c - 0xdc))
		if e != nil {
			return nil, e
		}
		return d.array(int(n64))
	case 0xde, 0xdf:
		n64, e := d.uint(2 << (c - 0xde))
		if e != nil {
			return nil, e
		}
		return d.object(int(n64))
	}
	return nil, fmt.Errorf("不支持的messagepack类型: 0x%x", c)
}

func (d *messagePackDecoder) length(code, base byte) (int, error) {
	width := 1 << (code - base)
	n, err := d.uint(width)
	return int(n), err
}
func (d *messagePackDecoder) string(n int) (any, error) { b, e := d.take(n); return string(b), e }
func (d *messagePackDecoder) array(n int) ([]any, error) {
	out := make([]any, n)
	for i := range out {
		v, e := d.value()
		if e != nil {
			return nil, e
		}
		out[i] = v
	}
	return out, nil
}
func (d *messagePackDecoder) object(n int) (map[string]any, error) {
	out := make(map[string]any, n)
	for i := 0; i < n; i++ {
		key, e := d.value()
		if e != nil {
			return nil, e
		}
		value, e := d.value()
		if e != nil {
			return nil, e
		}
		out[fmt.Sprint(key)] = value
	}
	return out, nil
}
