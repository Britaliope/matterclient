package matterclient

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

func (m *Client) parseResponse(rmsg *model.WebSocketResponse) {
	m.logger.Debugf("getting response: %#v", rmsg)
}

func (m *Client) DeleteMessage(postID string) error {
	_, err := m.Client.DeletePost(context.TODO(), postID)
	if err != nil {
		return err
	}

	return nil
}

func (m *Client) EditMessage(postID string, text string, props model.StringInterface) (string, error) {
	post := &model.Post{Message: text, Id: postID, Props: props}

	res, _, err := m.Client.UpdatePost(context.TODO(), postID, post)
	if err != nil {
		return "", err
	}

	return res.Id, nil
}

func (m *Client) GetFileLinks(filenames []string) []string {
	uriScheme := "https://"
	if m.NoTLS {
		uriScheme = "http://"
	}

	var output []string

	for _, f := range filenames {
		res, _, err := m.Client.GetFileLink(context.TODO(), f)
		if err != nil {
			// public links is probably disabled, create the link ourselves
			output = append(output, uriScheme+m.Credentials.Server+model.APIURLSuffix+"/files/"+f)

			continue
		}

		output = append(output, res)
	}

	return output
}

func (m *Client) GetPosts(channelID string, limit int) *model.PostList {
	for {
		res, resp, err := m.Client.GetPostsForChannel(context.TODO(), channelID, 0, limit, "", false, false)
		if err == nil {
			return res
		}

		if err := m.HandleRatelimit("GetPostsForChannel", resp); err != nil {
			return nil
		}
	}
}

func (m *Client) GetPostThread(postID string) *model.PostList {
	opts := model.GetPostsOptions{
		CollapsedThreads: false,
		Direction:        "up",
	}
	for {
		res, resp, err := m.Client.GetPostThreadWithOpts(context.TODO(), postID, "", opts)
		if err == nil {
			return res
		}

		if err := m.HandleRatelimit("GetPostThread", resp); err != nil {
			return nil
		}
	}
}

func (m *Client) GetPostsSince(channelID string, time int64) *model.PostList {
	for {
		res, resp, err := m.Client.GetPostsSince(context.TODO(), channelID, time, false)
		if err == nil {
			return res
		}

		if err := m.HandleRatelimit("GetPostsSince", resp); err != nil {
			return nil
		}
	}
}

func (m *Client) GetPublicLink(filename string) string {
	res, _, err := m.Client.GetFileLink(context.TODO(), filename)
	if err != nil {
		return ""
	}

	return res
}

func (m *Client) GetPublicLinks(filenames []string) []string {
	var output []string

	for _, f := range filenames {
		res, _, err := m.Client.GetFileLink(context.TODO(), f)
		if err != nil {
			continue
		}

		output = append(output, res)
	}

	return output
}

func (m *Client) PostMessage(channelID string, text string, rootID string, props model.StringInterface) (string, error) {
	post := &model.Post{
		ChannelId: channelID,
		Message:   text,
		RootId:    rootID,
		Props:     props,
	}

	for {
		res, resp, err := m.Client.CreatePost(context.TODO(), post)
		if err == nil {
			return res.Id, nil
		}

		if err := m.HandleRatelimit("CreatePost", resp); err != nil {
			return "", err
		}
	}
}

func (m *Client) PostMessageWithFiles(channelID string, text string, rootID string, fileIds []string, props model.StringInterface) (string, error) {
	post := &model.Post{
		ChannelId: channelID,
		Message:   text,
		RootId:    rootID,
		FileIds:   fileIds,
		Props:     props,
	}

	for {
		res, resp, err := m.Client.CreatePost(context.TODO(), post)
		if err == nil {
			return res.Id, nil
		}

		if err := m.HandleRatelimit("CreatePost", resp); err != nil {
			return "", err
		}
	}
}

func (m *Client) SearchPosts(query string) *model.PostList {
	res, _, err := m.Client.SearchPosts(context.TODO(), m.Team.ID, query, false)
	if err != nil {
		return nil
	}

	return res
}

// SendDirectMessage sends a direct message to specified user
func (m *Client) SendDirectMessage(toUserID string, msg string, rootID string) error {
	return m.SendDirectMessageProps(toUserID, msg, rootID, nil)
}

func (m *Client) SendDirectMessageProps(toUserID string, msg string, rootID string, props map[string]interface{}) error {
	m.logger.Debugf("SendDirectMessage to %s, msg %s", toUserID, msg)

	for {
		// create DM channel (only happens on first message)
		_, resp, err := m.Client.CreateDirectChannel(context.TODO(), m.User.Id, toUserID)
		if err == nil {
			break
		}

		if err := m.HandleRatelimit("CreateDirectChannel", resp); err != nil {
			m.logger.Debugf("SendDirectMessage to %#v failed: %s", toUserID, err)

			return err
		}
	}

	channelName := model.GetDMNameFromIds(toUserID, m.User.Id)

	// update our channels
	if err := m.UpdateChannels(); err != nil {
		m.logger.Errorf("failed to update channels: %#v", err)
	}

	// build & send the message
	msg = strings.ReplaceAll(msg, "\r", "")
	post := &model.Post{
		ChannelId: m.GetChannelID(channelName, m.Team.ID),
		Message:   msg,
		RootId:    rootID,
	}

	post.SetProps(props)

	for {
		_, resp, err := m.Client.CreatePost(context.TODO(), post)
		if err == nil {
			return nil
		}

		if err := m.HandleRatelimit("CreatePost", resp); err != nil {
			return err
		}
	}
}

func (m *Client) UploadFile(data []byte, channelID string, filename string) (string, error) {
	f, _, err := m.Client.UploadFile(context.TODO(), data, channelID, filename)
	if err != nil {
		return "", err
	}

	return f.FileInfos[0].Id, nil
}

func (m *Client) parseActionPost(rmsg *Message) {
	// add post to cache, if it already exists don't relay this again.
	// this should fix reposts
	if ok, _ := m.lruCache.ContainsOrAdd(digestString(rmsg.Raw.GetData()["post"].(string)), true); ok && rmsg.Raw.EventType() != model.WebsocketEventPostDeleted {
		m.logger.Debugf("message %#v in cache, not processing again", rmsg.Raw.GetData()["post"].(string))
		rmsg.Text = ""

		return
	}

	var data model.Post
	if err := json.NewDecoder(strings.NewReader(rmsg.Raw.GetData()["post"].(string))).Decode(&data); err != nil {
		return
	}
	// we don't have the user, refresh the userlist
	if m.GetUser(data.UserId) == nil {
		m.logger.Infof("User '%v' is not known, ignoring message '%#v'",
			data.UserId, data)
		return
	}

	rmsg.Username = m.GetUserName(data.UserId)
	rmsg.Channel = m.GetChannelName(data.ChannelId)
	rmsg.UserID = data.UserId
	rmsg.Type = data.Type
	teamid, _ := rmsg.Raw.GetData()["team_id"].(string)
	// edit messsages have no team_id for some reason
	if teamid == "" {
		// we can find the team_id from the channelid
		teamid = m.GetChannelTeamID(data.ChannelId)
		rmsg.Raw.GetData()["team_id"] = teamid
	}

	if teamid != "" {
		rmsg.Team = m.GetTeamName(teamid)
	}
	// direct message
	if rmsg.Raw.GetData()["channel_type"] == "D" {
		rmsg.Channel = m.GetUser(data.UserId).Username
	}

	rmsg.Text = data.Message
	rmsg.Post = &data
}

func (m *Client) parseMessage(rmsg *Message) {
	switch rmsg.Raw.EventType() {
	case model.WebsocketEventPosted, model.WebsocketEventPostEdited, model.WebsocketEventPostDeleted:
		m.parseActionPost(rmsg)
	case "user_updated":
		if user, ok := rmsg.Raw.GetData()["user"].(*model.User); ok {
			m.UpdateUser(user.Id)
		}
	case "group_added":
		if err := m.UpdateChannels(); err != nil {
			m.logger.Errorf("failed to update channels: %#v", err)
		}
	}
}

func digestString(s string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(s))) //nolint:gosec
}
