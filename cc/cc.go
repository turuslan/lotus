package cc

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/filecoin-project/lotus/blockstore"
	"github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"io"
	"io/ioutil"
	"os"
	"sort"
)

func ap(e error) { if e != nil { panic(e) } }

func UintBe16(b []byte) uint64 {
	return uint64(b[0]) << 8 + uint64(b[1])
}
func UintBe24(b []byte) uint64 {
	return uint64(b[0]) << 16 + UintBe16(b[1:1+2])
}
func UintBe40(b []byte) uint64 {
	return UintBe24(b[0:0+3]) << 16 + UintBe16(b[3:3+2])
}

const _32 = 32
const _40 = 40
type Key [_32]byte
type Row struct {
	Key Key
	Offset uint64
	MaxSize64 uint64
}
func (r Row) MaxSize() uint64 {
	return 64 * r.MaxSize64
}
type MemoryIndex struct {
	Raw []byte
}
func (index *MemoryIndex) Size() int {
	return len(index.Raw) / _40 - 2
}
func (index *MemoryIndex) Key(i int) Key {
	var k Key
	copy(k[:], index.Key2(i))
	return k
}
func (index *MemoryIndex) Key2(i int) []byte {
	return index.Row2(i)[:_32]
}
func (index *MemoryIndex) Offset(i int) uint64 {
	return UintBe40(index.Row2(i)[_32:_32+5])
}
func (index *MemoryIndex) MaxSize64(i int) uint64 {
	return UintBe24(index.Row2(i)[_32+5:_40])
}
func (index *MemoryIndex) Row(i int) Row {
	return Row{index.Key(i), index.Offset(i), index.MaxSize64(i)}
}
func (index *MemoryIndex) Row2(i int) []byte {
	j := (i + 1) * _40
	return index.Raw[j:j+_40]
}
func (index *MemoryIndex) Find(k Key) (bool, Row) {
	if o, i := index.Find2(k); o {
		return true, index.Row(i)
	}
	return false, Row{}
}
func (index *MemoryIndex) Find2(k Key) (bool, int) {
	i := sort.Search(index.Size(), func(i int) bool { return bytes.Compare(k[:], index.Key2(i)) <= 0 })
	if i < index.Size() && index.Key(i) == k {
		return true, i
	}
	return false, -1
}

var Prefix = []byte{0x01, 0x71, 0xA0, 0xE4, 0x02, 0x20}
func AsKey(c cid.Cid) (bool, Key) {
	var k Key
	b := c.Bytes()
	if bytes.HasPrefix(b, Prefix) && len(b) == len(Prefix) + _32 {
		copy(k[:], b[len(Prefix):])
		return true, k
	}
	return false, k
}

type CidsIpld struct {
	Index *MemoryIndex
	Car io.ReaderAt
	Ipld blockstore.Blockstore
}

func (c CidsIpld) Find(cid cid.Cid) ([]byte, bool) {
	if o, k := AsKey(cid); o {
		if o, r := c.Index.Find(k); o {
			rb := make([]byte, r.MaxSize())
			rn, re := c.Car.ReadAt(rb, int64(r.Offset))
			if re != nil && re != io.EOF { panic(re) }
			rb = rb[:rn]
			vv, vn := binary.Uvarint(rb)
			if vn == 0 { panic("CidsIpld.Get uvarint") }
			rb = rb[vn:]
			if len(rb) < int(vv) { panic("CidsIpld.Get not enough") }
			rb = rb[:vv]
			if !bytes.HasPrefix(rb, Prefix) { panic("CidsIpld.Get prefix") }
			rb = rb[len(Prefix):]
			if len(rb) < _32 { panic("CidsIpld.Get not enough") }
			if !bytes.HasPrefix(rb, k[:]) { panic("CidsIpld.Get key") }
			return rb[_32:], true
		}
	}
	return nil, false
}

var _ blockstore.Blockstore = &CidsIpld{}
func (c CidsIpld) View(cid cid.Cid, callback func([]byte) error) error {
	if e := c.Ipld.View(cid, callback); e == blockstore.ErrNotFound {
		if b, o := c.Find(cid); !o {
			return blockstore.ErrNotFound
		} else {
			return callback(b)
		}
	} else {
		return e
	}
}
func (c CidsIpld) DeleteMany(cids []cid.Cid) error {
	for _, cid := range cids {
		if e := c.DeleteBlock(cid); e != nil {
			return e
		}
	}
	return nil
}
func (c CidsIpld) DeleteBlock(cid cid.Cid) error {
	panic("CidsIpld.DeleteBlock")
}
func (c CidsIpld) Has2(cid cid.Cid) bool {
	if o, k := AsKey(cid); o {
		if o, _ := c.Index.Find2(k); o {
			return true
		}
	}
	return false
}
func (c CidsIpld) Has(cid cid.Cid) (bool, error) {
	if c.Has2(cid) {
		return true, nil
	}
	return c.Ipld.Has(cid)
}
func (c CidsIpld) Get(cid cid.Cid) (blocks.Block, error) {
	if b, o := c.Find(cid); o {
		return blocks.NewBlockWithCid(b, cid)
	}
	return c.Ipld.Get(cid)
}
func (c CidsIpld) GetSize(cid cid.Cid) (int, error) {
	panic("CidsIpld.GetSize")
}
func (c CidsIpld) Put(block blocks.Block) error {
	if c.Has2(block.Cid()) {
		return nil
	}
	return c.Ipld.Put(block)
}
func (c CidsIpld) PutMany(blocks []blocks.Block) error {
	w := 0
	for r, b := range blocks {
		if !c.Has2(b.Cid()) {
			blocks[w] = blocks[r]
			w++
		}
	}
	blocks = blocks[:w]
	return c.Ipld.PutMany(blocks)
}
func (c CidsIpld) AllKeysChan(ctx context.Context) (<-chan cid.Cid, error) {
	panic("CidsIpld.AllKeysChan")
}
func (c CidsIpld) HashOnRead(enabled bool) {
	c.Ipld.HashOnRead(enabled)
}

func MakeIndex(path string) *MemoryIndex {
	raw, e := ioutil.ReadFile(path); ap(e)
	return &MemoryIndex{raw}
}
func MakeIpld(car string, ipld blockstore.Blockstore) *CidsIpld {
	f, e := os.Open(car); ap(e)
	return &CidsIpld{MakeIndex(car + ".cids"), f, ipld}
}
