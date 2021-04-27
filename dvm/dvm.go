package dvm

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/exitcode"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-graphsync/ipldutil"
	cbornode "github.com/ipfs/go-ipld-cbor"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/multiformats/go-multihash"
	typegen "github.com/whyrusleeping/cbor-gen"
	"os"
	"strconv"
	"strings"
)

var Logging = false

var Logger *os.File

var indent = 0

func A() bool { return Logger != nil && Logging }

func Epanic(e error) {
	if e != nil {
		panic(e)
	}
}

func init() {
	var e error
	if path, ok := os.LookupEnv("DVM_LOG"); ok {
		Logger, e = os.Create(path)
		Epanic(e)
	}
}

func Indent() int {
	indent++
	return indent
}

func UnIndent(expected int) {
	if indent != expected {
		panic("indent mismatch")
	}
	indent--
}

func Log(s string) {
	if A() {
		if len(s) == 0 || s[len(s)-1] != '\n' {
			s += "\n"
		}
		s = strings.Repeat("  ", indent) + s
		Logger.WriteString(s)
	}
}

func Logf(f string, a ...interface{}) {
	if A() {
		Log(fmt.Sprintf(f, a...))
	}
}

func OnCharge(gas int64) {
	if gas != 0 {
		Logf("CHARGE %d", gas)
	}
}

func Addr(a *address.Address) string {
	s := a.String()
	s = "t" + s[1:]
	return s
}

func OnSend(method abi.MethodNum, nonce uint64, value *abi.TokenAmount, to, from *address.Address, params []byte) {
	if A() {
		Logf("SEND m=%d n=%d v=%s to=%s from=%s %s", method, nonce, value, Addr(to), Addr(from), DumpCbor(params))
	}
}

func SendTo(c cid.Cid) {
	if A() {
		m, e := multihash.Decode(c.Hash()); if e != nil { panic(e) }
		Logf("TO %s", string(m.Digest))
	}
}

func OnReceipt(exit exitcode.ExitCode, gas int64, ret []byte) {
	if A() {
		Logf("RECEIPT c=%d g=%d %s", exit, gas, DumpCbor(ret))
	}
}

func OnActor(store cbornode.IpldStore, addr *address.Address, nh *cid.Cid, nn uint64, nb *big.Int, c *cid.Cid, fo func() (*cid.Cid, uint64, *big.Int, bool)) {
	if A() {
		if oh, on, ob, ok := fo(); ok {
			if _h, _n, _b := !oh.Equals(*nh), on != nn, !ob.Equals(*nb); _b || _n || _h {
				m, e := multihash.Decode(c.Hash()); if e != nil { panic(e) }
				Logf("ACTOR %s %s", Addr(addr), string(m.Digest))
				defer UnIndent(Indent())
				if _b {
					Logf("balance %s -> %s", ob, nb)
				}
				if _n {
					Logf("nonce %d -> %d", on, nn)
				}
				if _h {
					Logf("HEAD %s -> %s", DumpCid(oh), DumpCid(nh))
					defer UnIndent(Indent())
					var os, ns typegen.Deferred
					if e := store.Get(context.Background(), *oh, &os); e != nil {
						Logf("???")
					} else {
						Logf("%s", DumpCbor(os.Raw))
					}
					if e := store.Get(context.Background(), *nh, &ns); e != nil {
						Logf("???")
					} else {
						Logf("%s", DumpCbor(ns.Raw))
					}
				}
			}
		}
	}
}

func OnIpldSet(c cid.Cid, b []byte) {
	if A() {
		Logf("IPLD PUT: %s %s", DumpCid(&c), DumpCbor(b))
	}
}

func DumpBytes(b []byte) string {
	return hex.EncodeToString(b)
}

func DumpCid(c *cid.Cid) string {
	mh, e := multihash.Decode(c.Hash())
	Epanic(e)
	if c.Version() == 1 && c.Type() == cid.DagCBOR && mh.Code == multihash.BLAKE2B_MIN+31 {
		return DumpBytes(mh.Digest)
	}
	b := c.Bytes()
	return DumpBytes(b[:len(b)-len(mh.Digest)-1]) + "_" + DumpBytes(mh.Digest)
}

func DumpCbor(b []byte) string {
	if len(b) == 0 {
		return "(empty)"
	}
	o := ""
	n, e := ipldutil.DecodeNode(b)
	if e != nil {
		if len(b) == 1 && b[0] == typegen.CborNull[0] {
			return "N"
		}
		return "(error:" + DumpBytes(b) + ")"
	}
	DumpCbor2(&o, n)
	return o
}

func DumpCbor2(o *string, n ipld.Node) {
	switch n.ReprKind() {
	case ipld.ReprKind_Link:
		_l, e := n.AsLink()
		Epanic(e)
		c := _l.(cidlink.Link).Cid
		*o += "@" + DumpCid(&c)
	case ipld.ReprKind_List:
		*o += "["
		l := n.ListIterator()
		for !l.Done() {
			i, v, e := l.Next()
			Epanic(e)
			if i != 0 {
				*o += ","
			}
			DumpCbor2(o, v)
		}
		*o += "]"
	case ipld.ReprKind_Map:
		*o += "{"
		m := n.MapIterator()
		c := false
		for !m.Done() {
			k, v, e := m.Next()
			Epanic(e)
			if c {
				*o += ","
			} else {
				c = true
			}
			DumpCbor2(o, k)
			*o += ":"
			DumpCbor2(o, v)
		}
		*o += "}"
	case ipld.ReprKind_Bytes:
		b, e := n.AsBytes()
		Epanic(e)
		if len(b) == 0 {
			*o += "~"
		} else {
			*o += DumpBytes(b)
		}
	case ipld.ReprKind_String:
		s, e := n.AsString()
		Epanic(e)
		*o += "^" + s
	case ipld.ReprKind_Int:
		i, e := n.AsInt()
		Epanic(e)
		if i >= 0 {
			*o += "+"
		}
		*o += strconv.Itoa(i)
	case ipld.ReprKind_Bool:
		b, e := n.AsBool()
		Epanic(e)
		if b {
			*o += "T"
		} else {
			*o += "F"
		}
	case ipld.ReprKind_Null:
		*o += "N"
	case ipld.ReprKind_Float:
		panic("float")
	}
}
