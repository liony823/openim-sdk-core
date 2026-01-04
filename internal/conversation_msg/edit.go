// Copyright © 2023 OpenIM SDK. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conversation_msg

import (
	"context"
	"errors"

	"github.com/openimsdk/openim-sdk-core/v3/pkg/common"
	"github.com/openimsdk/openim-sdk-core/v3/pkg/constant"
	"github.com/openimsdk/openim-sdk-core/v3/pkg/utils"
	"github.com/openimsdk/openim-sdk-core/v3/sdk_struct"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/utils/timeutil"

	"github.com/openimsdk/protocol/sdkws"
	"github.com/openimsdk/tools/log"
)

func (c *Conversation) doEditMsg(ctx context.Context, msg *sdkws.MsgData) error {
	var tips sdkws.EditMsgTips
	if err := utils.UnmarshalNotificationElem(msg.Content, &tips); err != nil {
		log.ZWarn(ctx, "unmarshal failed", err, "msg", msg)
		return errs.Wrap(err)
	}
	log.ZDebug(ctx, "do editMessage", "tips", &tips)
	return c.editMessage(ctx, &tips)
}

func (c *Conversation) editMessage(ctx context.Context, tips *sdkws.EditMsgTips) error {
	originalMsg, err := c.db.GetMessageBySeq(ctx, tips.ConversationID, tips.Seq)
	if err != nil {
		log.ZError(ctx, "GetMessageBySeq failed", err, "tips", &tips)
		return errs.Wrap(err)
	}

	var editorRole int32
	var editorNickname string
	if tips.IsAdminEdit || tips.SessionType == constant.SingleChatType {
		_, userName, err := c.getUserNameAndFaceURL(ctx, tips.EditorUserID)
		if err != nil {
			log.ZError(ctx, "GetUserNameAndFaceURL failed", err, "tips", &tips)
			return errs.Wrap(err)
		} else {
			log.ZDebug(ctx, "editor user name", "userName", userName)
		}

		editorNickname = userName
	} else if tips.SessionType == constant.ReadGroupChatType {
		conversation, err := c.db.GetConversation(ctx, tips.ConversationID)
		if err != nil {
			log.ZError(ctx, "GetConversation failed", err, "conversationID", tips.ConversationID)
			return errs.Wrap(err)
		}

		groupMember, err := c.group.GetSpecifiedGroupMembersInfo(ctx, conversation.GroupID, []string{tips.EditorUserID})
		if err != nil {
			log.ZError(ctx, "GetGroupMemberInfoByGroupIDUserID failed", err, "tips", &tips)
			return errs.Wrap(err)
		} else {
			log.ZDebug(ctx, "editor member name", "groupMember", groupMember)
			if len(groupMember) == 0 {
				editorNickname = "unknown"
			} else {
				editorRole = groupMember[0].RoleLevel
				editorNickname = groupMember[0].Nickname
			}
		}
	}

	m := sdk_struct.MessageEdited{
		EditorID:                    tips.EditorUserID,
		EditorRole:                  editorRole,
		ClientMsgID:                 originalMsg.ClientMsgID,
		EditorNickname:              editorNickname,
		EditTime:                    tips.EditTime,
		SourceMessageSendTime:       originalMsg.SendTime,
		SourceMessageSendID:         originalMsg.SendID,
		SourceMessageSenderNickname: originalMsg.SenderNickname,
		SessionType:                 tips.SessionType,
		Seq:                         tips.Seq,
		NewContent:                  tips.NewContent,
		ContentType:                 tips.ContentType,
		IsAdminEdit:                 tips.IsAdminEdit,
	}

	// Build the correct content based on content type
	var newContentStr string
	if tips.ContentType == constant.Text {
		// For text messages, wrap content in TextElem structure
		textElem := sdk_struct.TextElem{Content: tips.NewContent}
		newContentStr = utils.StructToJsonString(textElem)
	} else {
		// For other types, use the content as is
		newContentStr = tips.NewContent
	}

	// Update the message content with new content
	// Use UpdateColumnsMessage to ensure Content and Ex fields are updated even if they are empty strings
	if err := c.db.UpdateColumnsMessage(ctx, tips.ConversationID, originalMsg.ClientMsgID, map[string]interface{}{
		"content": newContentStr,
		"ex":      utils.StructToJsonString(m),
	}); err != nil {
		log.ZError(ctx, "UpdateColumnsMessage failed", err, "tips", &tips)
		return errs.Wrap(err)
	}

	conversation, err := c.db.GetConversation(ctx, tips.ConversationID)
	if err != nil {
		log.ZError(ctx, "GetConversation failed", err, "tips", &tips)
		return errs.Wrap(err)
	}

	// Update latest message if the edited message is the latest one
	var latestMsg sdk_struct.MsgStruct
	utils.JsonStringToStruct(conversation.LatestMsg, &latestMsg)
	log.ZDebug(ctx, "latestMsg", "latestMsg", &latestMsg, "seq", tips.Seq)
	if latestMsg.Seq == tips.Seq {
		msgs, err := c.db.GetMessageList(ctx, tips.ConversationID, 1, 0, 0, "", false)
		if err != nil || len(msgs) == 0 {
			log.ZError(ctx, "GetMessageListNoTime failed", err, "tips", &tips)
			return errs.Wrap(err)
		}
		log.ZDebug(ctx, "latestMsg is edited", "seq", tips.Seq, "msg", msgs[0])
		newLatestMsg := *LocalChatLogToMsgStruct(msgs[0])
		log.ZDebug(ctx, "edit update conversation", "msg", utils.StructToJsonString(newLatestMsg))
		if err := c.db.UpdateColumnsConversation(ctx, tips.ConversationID, map[string]interface{}{
			"latest_msg":           utils.StructToJsonString(newLatestMsg),
			"latest_msg_send_time": newLatestMsg.SendTime,
		}); err != nil {
			log.ZError(ctx, "UpdateColumnsConversation failed", err, "newLatestMsg", newLatestMsg)
		} else {
			c.doUpdateConversation(common.Cmd2Value{Value: common.UpdateConNode{Action: constant.ConChange, Args: []string{tips.ConversationID}}})
		}
	}

	// Get the updated message and trigger callback
	updatedMsg, err := c.db.GetMessageBySeq(ctx, tips.ConversationID, tips.Seq)
	if err != nil {
		log.ZError(ctx, "GetMessageBySeq failed after update", err, "tips", &tips)
		// Still trigger callback with MessageEdited info even if we can't get the message
		c.msgListener().OnNewRecvMessageEdited(utils.StructToJsonString(m))
		return errs.Wrap(err)
	}

	// Convert to MsgStruct and trigger callback with full message
	msgStruct := LocalChatLogToMsgStruct(updatedMsg)
	c.msgListener().OnNewRecvMessageEdited(utils.StructToJsonString(msgStruct))

	return nil
}

func (c *Conversation) editOneMessage(ctx context.Context, conversationID, clientMsgID, newContent string, contentType int32) error {
	conversation, err := c.db.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	message, err := c.waitForMessageSyncSeq(ctx, conversationID, clientMsgID)
	if err != nil {
		return err
	}
	if message.Status != constant.MsgStatusSendSuccess {
		return errors.New("only send success message can be edited")
	}

	// Check permissions
	switch conversation.ConversationType {
	case constant.SingleChatType:
		if message.SendID != c.loginUserID {
			return errors.New("only send by yourself message can be edited")
		}
	case constant.ReadGroupChatType:
		if message.SendID != c.loginUserID {
			groupAdmins, err := c.db.GetGroupMemberOwnerAndAdminDB(ctx, conversation.GroupID)
			if err != nil {
				return err
			}
			var isAdmin bool
			for _, member := range groupAdmins {
				if member.UserID == c.loginUserID {
					isAdmin = true
					break
				}
			}
			if !isAdmin {
				return errors.New("only group admin can edit message")
			}
		}
	}

	err = c.editMessageOnServer(ctx, conversationID, message.Seq, newContent, contentType)
	if err != nil {
		return err
	}

	c.editMessage(ctx, &sdkws.EditMsgTips{
		ConversationID: conversationID,
		Seq:            message.Seq,
		EditorUserID:   c.loginUserID,
		EditTime:       timeutil.GetCurrentTimestampByMill(),
		SessionType:    conversation.ConversationType,
		ClientMsgID:    clientMsgID,
		NewContent:     newContent,
		ContentType:    contentType,
	})
	return nil
}
