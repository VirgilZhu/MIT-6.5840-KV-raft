package kvraft

import (
	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const Debug = false

const (
	HandleOpTimeOut = time.Millisecond * 500
)

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

type OType int

const (
	OPGet OType = iota
	OPPut
	OPAppend
)

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	OpType     OType
	Key        string
	Val        string
	Seq        uint64
	Identifier int64
}

type result struct {
	LastSeq uint64
	Err     Err
	Value   string
	ResTerm int
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	waiCh      map[int]*chan result // 映射 startIndex->ch
	historyMap map[int64]*result    // 映射 Identifier->*result
	maxMapLen  int
	db         map[string]string
}

func (kv *KVServer) LogInfoReceive(opArgs *Op, logType int) {
	// logType:
	// 	0: 新的请求
	// 	1: 重复的请求
	// 	2: 旧的请求
	needPanic := false
	dateStr := ""
	if logType == 0 {
		dateStr = "新的"
	} else if logType == 1 {
		dateStr = "重复"
	} else {
		dateStr = "旧的"
		needPanic = true
	}
	switch opArgs.OpType {
	case OPGet:
		DPrintf("leader %v identifier %v Seq %v %sGet请求: Get(%v),\n", kv.me, opArgs.Identifier, opArgs.Seq, dateStr, opArgs.Key)
	case OPPut:
		DPrintf("leader %v identifier %v Seq %v %sPut请求: Put(%v,%v),\n", kv.me, opArgs.Identifier, opArgs.Seq, dateStr, opArgs.Key, opArgs.Val)
	case OPAppend:
		DPrintf("leader %v identifier %v Seq %v %sPut请求: Put(%v,%v),\n", kv.me, opArgs.Identifier, opArgs.Seq, dateStr, opArgs.Key, opArgs.Val)
	}

	if needPanic {
		panic("没有记录更早的请求的结果")
	}
}

func (kv *KVServer) LogInfoDBExecute(opArgs *Op, err Err, res string, isLeader bool) {
	role := "follower"
	if isLeader {
		role = "leader"
	}
	switch opArgs.OpType {
	case OPGet:
		if err != "" {
			DPrintf("%s %v identifier %v Seq %v DB执行Get请求: Get(%v), Err=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, err)
		} else {
			DPrintf("%s %v identifier %v Seq %v DB执行Get请求: Get(%v), res=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, res)
		}
	case OPPut:
		if err != "" {
			DPrintf("%s %v identifier %v Seq %v DB执行Put请求: Put(%v,%v), Err=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, opArgs.Val, err)

		} else {
			DPrintf("%s %v identifier %v Seq %v DB执行Put请求: Put(%v,%v), res=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, opArgs.Val, res)
		}
	case OPAppend:
		if err != "" {
			DPrintf("%s %v identifier %v Seq %v DB执行Append请求: Put(%v,%v), Err=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, opArgs.Val, err)
		} else {
			DPrintf("%s %v identifier %v Seq %v DB执行Append请求: Put(%v,%v), res=%s\n", role, kv.me, opArgs.Identifier, opArgs.Seq, opArgs.Key, opArgs.Val, res)
		}
	}
}

func (kv *KVServer) DBExecute(op *Op, isLeader bool) (res result) {
	// 调用该函数需要持有锁
	res.LastSeq = op.Seq
	switch op.OpType {
	case OPGet:
		val, exist := kv.db[op.Key]
		if exist {
			kv.LogInfoDBExecute(op, "", val, isLeader)
			res.Value = val
			return
		} else {
			res.Err = ErrKeyNotExist
			res.Value = ""
			kv.LogInfoDBExecute(op, "", ErrKeyNotExist, isLeader)
			return
		}
	case OPPut:
		kv.db[op.Key] = op.Val
		kv.LogInfoDBExecute(op, "", kv.db[op.Key], isLeader)
		return
	case OPAppend:
		val, exist := kv.db[op.Key]
		if exist {
			kv.db[op.Key] = val + op.Val
			kv.LogInfoDBExecute(op, "", kv.db[op.Key], isLeader)
			return
		} else {
			kv.db[op.Key] = op.Val
			kv.LogInfoDBExecute(op, "", kv.db[op.Key], isLeader)
			return
		}
	}
	return
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrNotLeader
		return
	}
	opArgs := &Op{OpType: OPGet, Seq: args.Seq, Key: args.Key, Identifier: args.Identifier}

	res := kv.HandleOp(opArgs)
	reply.Err = res.Err
	reply.Value = res.Value
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrNotLeader
		return
	}

	opArgs := &Op{Seq: args.Seq, Key: args.Key, Val: args.Value, Identifier: args.Identifier}
	if args.Op == "Put" {
		opArgs.OpType = OPPut
	} else {
		opArgs.OpType = OPAppend
	}

	res := kv.HandleOp(opArgs)
	reply.Err = res.Err
}

func (kv *KVServer) HandleOp(opArgs *Op) (res result) {
	startIndex, startTerm, isLeader := kv.rf.Start(*opArgs)
	if !isLeader {
		return result{Err: ErrNotLeader, Value: ""}
	}

	kv.mu.Lock()

	// 直接覆盖之前记录的chan
	newCh := make(chan result)
	kv.waiCh[startIndex] = &newCh
	DPrintf("leader %v identifier %v Seq %v 的请求: 新建管道: %p\n", kv.me, opArgs.Identifier, opArgs.Seq, &newCh)
	kv.mu.Unlock() // Start函数耗时较长, 先解锁

	defer func() {
		kv.mu.Lock()
		delete(kv.waiCh, startIndex)
		close(newCh)
		kv.mu.Unlock()
	}()

	// 等待消息到达或超时
	select {
	case <-time.After(HandleOpTimeOut):
		res.Err = ErrHandleOpTimeOut
		DPrintf("server %v identifier %v Seq %v: 超时", kv.me, opArgs.Identifier, opArgs.Seq)
		return
	case msg, success := <-newCh:
		if success && msg.ResTerm == startTerm {
			res = msg
			return
		} else if !success {
			// 通道已经关闭, 有另一个协程收到了消息 或 通道被更新的RPC覆盖
			DPrintf("server %v identifier %v Seq %v: 通道已经关闭, 有另一个协程收到了消息 或 更新的RPC覆盖, args.OpType=%v, args.Key=%+v", kv.me, opArgs.Identifier, opArgs.Seq, opArgs.OpType, opArgs.Key)
			res.Err = ErrChanClose
			return
		} else {
			// term与一开始不匹配, 说明这个Leader可能过期了
			DPrintf("server %v identifier %v Seq %v: term与一开始不匹配, 说明这个Leader可能过期了, res.ResTerm=%v, startTerm=%+v", kv.me, opArgs.Identifier, opArgs.Seq, res.ResTerm, startTerm)
			res.Err = ErrLeaderOutDated
			res.Value = ""
			return
		}
	}
}

func (kv *KVServer) ApplyHandler() {
	for !kv.killed() {
		log := <-kv.applyCh

		if log.CommandValid {

			op := log.Command.(Op)
			kv.mu.Lock()

			// 需要判断这个log是否需要被再次应用
			var res result

			needApply := false
			if hisMap, exist := kv.historyMap[op.Identifier]; exist {
				if hisMap.LastSeq == op.Seq {
					// 历史记录存在且Seq相同, 直接套用历史记录
					res = *hisMap
				} else if hisMap.LastSeq < op.Seq {
					// 否则新建
					needApply = true
				}
			} else {
				// 历史记录不存在
				needApply = true
			}

			_, isLeader := kv.rf.GetState()

			if needApply {
				// 执行log
				res = kv.DBExecute(&op, isLeader)
				res.ResTerm = log.SnapshotTerm

				// 更新历史记录
				kv.historyMap[op.Identifier] = &res
			}

			if !isLeader {
				// 不是leader则继续检查下一个log
				kv.mu.Unlock()
				continue
			}

			// Leader还需要额外通知handler处理clerk回复
			ch, exist := kv.waiCh[log.CommandIndex]
			if !exist {
				// 接收端的通道已经被删除了并且当前节点是 leader, 说明这是重复的请求, 但这种情况不应该出现, 所以panic
				DPrintf("leader %v ApplyHandler 发现 identifier %v Seq %v 的管道不存在, 应该是超时被关闭了", kv.me, op.Identifier, op.Seq)
				kv.mu.Unlock()
				continue
			}
			kv.mu.Unlock()
			// 发送消息
			func() {
				defer func() {

					if recover() != nil {
						// 如果这里有 panic，是因为通道关闭
						DPrintf("leader %v ApplyHandler 发现 identifier %v Seq %v 的管道不存在, 应该是超时被关闭了", kv.me, op.Identifier, op.Seq)
					}
				}()
				res.ResTerm = log.SnapshotTerm
				*ch <- res
			}()
		}
	}
}

// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	// You may need initialization code here.
	kv.historyMap = make(map[int64]*result)
	kv.db = make(map[string]string)
	kv.waiCh = make(map[int]*chan result)

	go kv.ApplyHandler()

	return kv
}
