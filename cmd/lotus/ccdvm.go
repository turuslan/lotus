package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/fatih/color"
	"github.com/filecoin-project/lotus/blockstore"
	"github.com/filecoin-project/lotus/cc"
	"github.com/filecoin-project/lotus/chain/stmgr"
	"github.com/filecoin-project/lotus/chain/store"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/vm"
	"github.com/filecoin-project/lotus/extern/sector-storage/ffiwrapper"
	"github.com/filecoin-project/lotus/journal"
	"github.com/filecoin-project/lotus/node/repo"
	"github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-car"
	"github.com/ipld/go-car/util"
	"github.com/urfave/cli/v2"
	"os"
	"sync"
)

type _Cids = map[cid.Cid]bool
type Cids struct {
	Cids _Cids
	C chan cid.Cid
	M sync.Mutex
}
func MakeCids() *Cids {
	cs := &Cids{make(_Cids), make(chan cid.Cid), sync.Mutex{}}
	go func() {
		cs.M.Lock()
		defer cs.M.Unlock()
		for {
			if c, h := <-cs.C; h {
				cs.Cids[c] = true
			} else {
				break
			}
		}
	}()
	return cs
}
func (cs *Cids) Close() {
	close(cs.C)
	cs.M.Lock()
	cs.M.Unlock()
}
func MergeCids(l, r _Cids) _Cids {
	o := make(_Cids)
	for c := range l {
		o[c] = true
	}
	for c := range r {
		o[c] = true
	}
	return o
}

type HookIpld struct {
	blockstore.Blockstore
	R *Cids
	W *Cids
}
func MakeHookIpld(bs blockstore.Blockstore) *HookIpld {
	return &HookIpld{bs, MakeCids(), MakeCids()}
}
func (bs *HookIpld) Close() {
	bs.R.Close()
	bs.W.Close()
}

func (bs *HookIpld) View(c cid.Cid, callback func([]byte) error) error {
	return bs.Blockstore.View(c, func(raw []byte) error {
		bs.R.C <- c
		return callback(raw)
	})
}
func (bs *HookIpld) Get(c cid.Cid) (blocks.Block, error) {
	if out, err := bs.Blockstore.Get(c); err != nil {
		return nil, err
	} else {
		bs.R.C <- c
		return out, nil
	}
}
func (bs *HookIpld) Put(block blocks.Block) error {
	bs.W.C <- block.Cid()
	return bs.Blockstore.Put(block)
}
func (bs *HookIpld) PutMany(blocks []blocks.Block) error {
	for _, block := range blocks {
		bs.W.C <- block.Cid()
	}
	return bs.Blockstore.PutMany(blocks)
}

func ap(e error) { if e != nil { panic(e) } }
var CcDvmCmd = &cli.Command{
	Name:  "ccdvm",
	Action: func(cctx *cli.Context) error {
		_cid := func(a string) cid.Cid { c, e := cid.Decode(a); ap(e); return c }

		REPO := ".ccdvm"
		CARS := cctx.Args().Slice()
		CTS := _cid(os.Getenv("CTS"))
		HOOK := os.Getenv("HOOK")
		tag := color.GreenString("[main]")

		fmt.Printf("%s init\n", tag)
		r, e := repo.NewFS(REPO); ap(e)
		e = r.Init(repo.FullNode); if e != repo.ErrRepoExists { ap(e) }
		lr, e := r.Lock(repo.FullNode); ap(e)
		defer lr.Close()
		_bs, e := lr.Blockstore(cctx.Context, repo.UniversalBlockstore)

		bs := _bs
		var _ccs []*cc.CidsIpld
		var _cs *cc.CidsIpld
		for _, car := range CARS {
			_cs = cc.MakeIpld(car, bs)
			fmt.Printf("%s index: %dmb, %d rows, %s\n", tag, len(_cs.Index.Raw) >> 20, _cs.Index.Size(), car)
			_ccs = append(_ccs, _cs)
			bs = _cs
		}

		var hook *HookIpld
		if HOOK != "" {
			hook = MakeHookIpld(bs)
			bs = hook
		}

		fmt.Printf("%s init\n", tag)
		mds, e := lr.Datastore(context.TODO(), "/metadata"); ap(e)
		j, e := journal.OpenFSJournal(lr, journal.EnvDisabledEvents()); ap(e)
		cst := store.NewChainStore(bs, bs, mds, vm.Syscalls(ffiwrapper.ProofVerifier), j)
		stm := stmgr.NewStateManager(cst)
		genesis, e := cst.LoadTipSet(types.NewTipSetKey(_cid("bafy2bzacecnamqgqmifpluoeldx7zzglxcljo6oja4vrmtj7432rphldpdmm2"))); ap(e)
		e = cst.SetGenesis(genesis.Blocks()[0]); ap(e)
		head, e := cst.LoadTipSet(types.NewTipSetKey(CTS)); ap(e)
		cts := head
		pts, e := cst.LoadTipSet(cts.Parents()); ap(e)

		fmt.Printf("%s interpret %d(^%d)\n", tag, pts.Height(), cts.Height())
		as, ar, e := stm.TipSetState(cctx.Context, pts)
		if e != nil {
			fmt.Printf("%s %s: %s", tag, color.RedString("interpret"), e)
		} else if as != cts.ParentState() {
			fmt.Printf("%s %s: states", tag, color.RedString("interpret"))
		} else if ar != cts.ParentReceipts() {
			fmt.Printf("%s %s: receipts", tag, color.RedString("interpret"))
		} else {
			fmt.Printf("%s interpret: ok\n", tag)
		}

		if hook != nil {
			hook.Close()
			cs := MergeCids(hook.R.Cids, hook.W.Cids)
			fmt.Printf("%s hook: %d r + %d w = %d, %s\n", tag, len(hook.R.Cids), len(hook.W.Cids), len(cs), HOOK)
			_f, e := os.Create(HOOK); ap(e)
			f := bufio.NewWriterSize(_f, 64 << 10)
			e = car.WriteHeader(&car.CarHeader{[]cid.Cid{CTS}, 1}, f); ap(e)
			for c := range hook.W.Cids {
				delete(hook.R.Cids, c)
			}
			for c := range cs {
				v, e := hook.Blockstore.Get(c); ap(e)
				e = util.LdWrite(f, c.Bytes(), v.RawData()); ap(e)
			}
			e = f.Flush(); ap(e)
			_f.Close()
		}

		fmt.Printf("%s done\n", tag)
		return nil
	},
}
