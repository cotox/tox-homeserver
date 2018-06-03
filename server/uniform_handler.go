package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopp"
	"log"
	"strconv"
	"tox-homeserver/common"
	"tox-homeserver/thspbs"

	tox "github.com/TokTok/go-toxcore-c"
	"github.com/envsh/go-toxcore/xtox"
)

//

func packBaseInfo(t *tox.Tox) (*thspbs.BaseInfo, error) {

	out := &thspbs.BaseInfo{}
	out.ToxId = t.SelfGetAddress()
	out.Name = t.SelfGetName()
	out.Stmsg, _ = t.SelfGetStatusMessage()
	out.Status = uint32(t.SelfGetStatus())
	out.ConnStatus = int32(t.SelfGetConnectionStatus())
	out.Friends = make(map[uint32]*thspbs.FriendInfo)
	out.Groups = make(map[uint32]*thspbs.GroupInfo)

	fns := t.SelfGetFriendList()
	for _, fn := range fns {
		pubkey, _ := t.FriendGetPublicKey(fn)
		fname, _ := t.FriendGetName(fn)
		stmsg, _ := t.FriendGetStatusMessage(fn)
		fstatus, _ := t.FriendGetConnectionStatus(fn)

		fi := &thspbs.FriendInfo{}
		fi.Pubkey = pubkey
		fi.Fnum = fn
		fi.Name = fname
		fi.Stmsg = stmsg
		fi.Status = uint32(fstatus)
		fi.ConnStatus = int32(fstatus)

		out.Friends[fn] = fi
	}

	gns := t.ConferenceGetChatlist()
	for _, gn := range gns {
		title, err := t.ConferenceGetTitle(gn)
		gopp.ErrPrint(err, title)

		mtype, err := t.ConferenceGetType(gn)
		gopp.ErrPrint(err, mtype)

		groupId, _ := xtox.ConferenceGetIdentifier(t, gn)

		gi := &thspbs.GroupInfo{}
		gi.Members = make(map[uint32]*thspbs.MemberInfo)
		gi.Gnum = gn
		gi.GroupId = groupId
		gi.Title = title
		gi.Ours = !xtox.IsInvitedGroup(t, gn)
		gi.Mtype = uint32(mtype)

		pcnt := t.ConferencePeerCount(gn)
		for i := uint32(0); i < pcnt; i++ {
			pname, err := t.ConferencePeerGetName(gn, i)
			gopp.ErrPrint(err, pname)
			if pname == "" {
				pname = common.DefaultUserName
			}
			ppubkey, err := t.ConferencePeerGetPublicKey(gn, i)
			gopp.ErrPrint(err, ppubkey)

			mi := &thspbs.MemberInfo{}
			mi.Pnum = i
			mi.Pubkey = ppubkey
			mi.Name = pname

			gi.Members[i] = mi
		}

		out.Groups[gn] = gi
	}

	id, err := appctx.st.MaxEventId()
	gopp.ErrPrint(err)
	out.NextBatch = id + 1

	return out, nil
}

// 自己的消息做多终端同步转发
// conn caller connection
func RmtCallHandlers(ctx context.Context, req *thspbs.Event) (*thspbs.Event, error) {
	log.Println(req.EventId, req.Name, req.Args, req.Margs)

	// 先把消息同步到不同协议的不同终端上, not need execute result
	switch req.Name {
	case "LoadEventsByContactId":
	default:
	case "SelfSetName", "SelfSetStatusMessage":
		fallthrough
	case "FriendSendMessage":
		fallthrough
	case "ConferenceSendMessage":
		out, err := RmtCallResyncHandler(context.Background(), req)
		gopp.ErrPrint(err)
		if err == nil {
			pubmsgall(ctx, out)
		}
	}

	rsp, err := RmtCallExecuteHandler(ctx, req)

	// re sync to client, need execute result
	switch req.Name {
	case "FriendAdd": //
		fallthrough
	case "FriendAddNorequest":
		fallthrough
	case "FriendDelete": //
		fallthrough
	case "ConferenceNew":
		fallthrough
	case "ConferenceDelete":
		rsp.Margs = append(rsp.Margs, req.Args...)
		pubmsgall(ctx, rsp)
	}

	return rsp, err
}

// 直接执行请求
func RmtCallExecuteHandler(ctx context.Context, req *thspbs.Event) (*thspbs.Event, error) {
	out := &thspbs.Event{Name: req.Name + "Resp"}

	var err error
	t := appctx.tvm.t
	switch req.Name {
	case "GetBaseInfo":
		binfo, err := packBaseInfo(t)
		gopp.ErrPrint(err)
		bdata, err := json.Marshal(binfo)
		gopp.ErrPrint(err)
		out.Args = []string{string(bdata)}
	case "FriendSendMessage": // args: "friendNumber" or "friendPubkey", "msg"
		if len(req.Args) < 2 {
			log.Println("paramter error")
		}
		fnum := uint32(gopp.MustInt(req.Args[0]))
		if len(req.Args[0]) >= 64 { // think as friendPubkey
			fnum, _ = t.FriendByPublicKey(req.Args[0])
			log.Println(fnum, " <- ", req.Args[0])
		}
		log.Println("fnum:", fnum, req.Args)
		wn, err := t.FriendSendMessage(fnum, req.Args[1])
		gopp.ErrPrint(err)
		pubkey := t.SelfGetPublicKey()
		msgo, err := appctx.st.AddFriendMessage(req.Args[1], pubkey, req.EventId)
		gopp.ErrPrint(err)
		out.EventId = msgo.EventId
		out.Args = append(out.Args, fmt.Sprintf("%d", wn))

		// groups
	case "ConferenceNew": // args:name,returns:
		rname := req.Args[0]
		gn, err := t.ConferenceNew()
		gopp.ErrPrint(err, rname)
		out.EventId = int64(gn)
		_, err = t.ConferenceSetTitle(gn, rname)
		gopp.ErrPrint(err, gn, rname)
		groupId, _ := t.ConferenceGetIdentifier(gn)
		gopp.Assert(!xtox.ConferenceIdIsEmpty(groupId), rname, gn)
		// TODO not needed
		gopp.CondWait(10, func() bool {
			t.Iterate2(nil)
			groupId, _ = t.ConferenceGetIdentifier(gn)
			log.Println(gn, groupId)
			return !xtox.ConferenceIdIsEmpty(groupId)
		})
		out.Args = append(out.Args, groupId, gopp.ToStr(gn))

		_, err = appctx.st.AddGroup(groupId, gn, rname)
		gopp.ErrPrint(err, gn, rname, groupId)

	case "ConferenceDelete": // "groupNumber"
		gnum := gopp.MustUint32(req.Args[0])
		groupId, _ := t.ConferenceGetIdentifier(gnum)
		title, _ := t.ConferenceGetTitle(gnum)
		_, err = t.ConferenceDelete(gnum)
		gopp.ErrPrint(err, req.Args)
		out.Args = append(out.Args, groupId)
		log.Println(req.Name, req.Args[0], title, groupId)

	case "ConferenceSendMessage": // "groupNumber" or groupIdentity,"mtype","msg", optional4web("groupTitle")
		gnum := uint32(gopp.MustInt(req.Args[0]))
		mtype := gopp.MustInt(req.Args[1])
		if len(req.Args[0]) > 10 { // think as groupIdentity
			gnum, _ = xtox.ConferenceGetByIdentifier(t, req.Args[0])
			log.Println(gnum, " <- ", req.Args[0])
		}
		_, err = t.ConferenceSendMessage(gnum, mtype, req.Args[2])
		gopp.ErrPrint(err)
		if err != nil {
			out.ErrCode = -1
			out.ErrMsg = err.Error()
		}
		identifier, _ := xtox.ConferenceGetIdentifier(t, uint32(gnum))
		pubkey := t.SelfGetPublicKey()
		msgo, err := appctx.st.AddGroupMessage(req.Args[2], "0", identifier, pubkey, req.EventId)
		gopp.ErrPrint(err)
		out.EventId = msgo.EventId
	case "ConferenceJoin": // friendNumber, cookie
		fnum := gopp.MustUint32(req.Args[0])
		cookie := req.Args[1]
		gn, err := t.ConferenceJoin(fnum, cookie)
		gopp.ErrPrint(err, fnum, len(cookie))
		out.EventId = int64(gn)

	case "ConferencePeerCount": // groupNumber
		gnum := gopp.MustUint32(req.Args[0])
		cnt := t.ConferencePeerCount(gnum)
		out.EventId = int64(cnt)
	case "ConferencePeerGetName": // groupNumber, peerNumber
		gnum := gopp.MustUint32(req.Args[0])
		pnum := gopp.MustUint32(req.Args[1])
		pname, err := t.ConferencePeerGetName(gnum, pnum)
		gopp.ErrPrint(err, req.Args)
		out.Args = append(out.Args, pname)
	case "ConferenceInvite": // groupNumber, friendNumber
		gnum := gopp.MustUint32(req.Args[0])
		fnum := gopp.MustUint32(req.Args[1])
		_, err := t.ConferenceInvite(fnum, gnum)
		gopp.ErrPrint(err, req.Args)
	case "FriendAdd": // toxid, addmsg
		toxid := req.Args[0]
		addmsg := req.Args[1]
		frndno, err := t.FriendAdd(toxid, addmsg)
		gopp.ErrPrint(err, toxid, addmsg)
		if err != nil {
			out.ErrCode = -1
			out.ErrMsg = err.Error()
		} else {
			out.Args = []string{gopp.ToStr(frndno)}
			err := xtox.WriteSavedata(t, tvmCtx.SaveFile)
			gopp.ErrPrint(err)
		}
	case "FriendAddNorequest": // toxid
		toxid := req.Args[0]
		frndno, err := t.FriendAddNorequest(toxid)
		gopp.ErrPrint(err, toxid)
		if err != nil {
			out.ErrCode = -1
			out.ErrMsg = err.Error()
		} else {
			out.Args = []string{gopp.ToStr(frndno)}
			err := xtox.WriteSavedata(t, tvmCtx.SaveFile)
			gopp.ErrPrint(err)
		}
	case "FriendDelete": // frndno
		frndno := gopp.MustUint32(req.Args[0])
		pubkey, _ := t.FriendGetPublicKey(frndno)
		out.Margs = []string{pubkey}
		_, err := t.FriendDelete(frndno)
		gopp.ErrPrint(err, frndno)
		if err != nil {
			out.ErrCode, out.ErrMsg = -1, err.Error()
			err := xtox.WriteSavedata(t, tvmCtx.SaveFile)
			gopp.ErrPrint(err)
		}
	case "SelfSetName":
		name := req.Args[0]
		err := t.SelfSetName(name)
		gopp.ErrPrint(err, name)
	case "SelfSetStatusMessage":
		stmsg := req.Args[0]
		_, err := t.SelfSetStatusMessage(stmsg)
		gopp.ErrPrint(err, stmsg)
	// case "GetHistory":
	case "PullEventsByContactId":
		prev_batch, err := strconv.Atoi(req.Args[1])
		gopp.ErrPrint(err, req.Args)
		if err == nil {
			msgos, err := appctx.st.FindEventsByContactId2(req.Args[0], int64(prev_batch), common.PullPageSize)
			gopp.ErrPrint(err)
			if err == nil {
				data, err := json.Marshal(msgos)
				gopp.ErrPrint(err)
				out.Args = []string{string(data)}
			}
		}
	default:
		log.Println("unimpled:", req.Name, req.Args)
		out.ErrCode = -1
		out.ErrMsg = fmt.Sprintf("Unimpled: %s", req.Name)
	}

	common.BytesRecved(len(req.String()))
	common.BytesSent(len(out.String()))
	return out, nil
}

// 自己的消息做多终端同步转发
// 把其中一个端发送的消息再同步到其他端上
// 需要记录一个终端的id
func RmtCallResyncHandler(ctx context.Context, req *thspbs.Event) (*thspbs.Event, error) {
	log.Println(req.EventId, req.Name, req.Args, req.Margs)
	out := &thspbs.Event{}

	var err error
	t := appctx.tvm.t
	st := appctx.st
	err = gopp.DeepCopy(req, out)
	gopp.ErrPrint(err)

	switch req.Name {
	case "FriendSendMessage": // args: "friendNumber" or "friendPubkey", "msg"
		fnum := uint32(gopp.MustInt(req.Args[0]))
		if len(req.Args[0]) >= 64 { // think as friendPubkey
			fnum, _ = t.FriendByPublicKey(req.Args[0])
			log.Println(fnum, " <- ", req.Args[0])
		} else {
		}
		fname, err := t.FriendGetName(fnum)
		gopp.ErrPrint(err)
		pubkey, err := t.FriendGetPublicKey(fnum)
		gopp.ErrPrint(err)
		eventId := st.NextId()
		req.EventId = eventId
		out.Margs = []string{fname, pubkey, gopp.ToStr(eventId)}

	//  依赖执行结果，不能转。而在处理的时候获取Resp
	// case "FriendAdd": // args: toxid, addmsg
	// case "FriendAddNorequest": // args: toxid

	case "ConferenceSendMessage": // "groupNumber" or groupIdentity,"mtype","msg"
		gnum := uint32(gopp.MustInt(req.Args[0]))
		if len(req.Args[0]) > 10 { // think as groupIdentity
			gnum, _ = xtox.ConferenceGetByIdentifier(t, req.Args[0])
			log.Println(gnum, " <- ", req.Args[0])
		} else {
		}
		title, err := t.ConferenceGetTitle(gnum)
		gopp.ErrPrint(err)

		groupId, _ := xtox.ConferenceGetIdentifier(t, gnum)
		peerPubkey := t.SelfGetPublicKey()
		peerName := t.SelfGetName()
		eventId := st.NextId()
		req.EventId = eventId
		out.Margs = []string{peerName, peerPubkey, title, groupId, gopp.ToStr(eventId)}
	default:
		return nil, errors.New("not need/impl resync " + req.Name)
	}

	return out, nil
}
