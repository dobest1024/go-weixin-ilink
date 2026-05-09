package ilink

import "context"

type notifyStartRequest struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type notifyStartResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

type notifyStopRequest struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type notifyStopResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

func notifyStart(ctx context.Context, c *client, baseInfo *BaseInfo) error {
	req := &notifyStartRequest{BaseInfo: baseInfo}
	var resp notifyStartResponse
	if err := c.post(ctx, "/ilink/bot/msg/notifystart", req, &resp); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return &APIError{Code: resp.Ret, Message: resp.ErrMsg}
	}
	return nil
}

func notifyStop(ctx context.Context, c *client, baseInfo *BaseInfo) error {
	req := &notifyStopRequest{BaseInfo: baseInfo}
	var resp notifyStopResponse
	if err := c.post(ctx, "/ilink/bot/msg/notifystop", req, &resp); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return &APIError{Code: resp.Ret, Message: resp.ErrMsg}
	}
	return nil
}
