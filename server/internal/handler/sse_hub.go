package handler

import (
	"encoding/json"
	"strings"
	"sync"
)

// sseEvent 是推送给前端 SSE 的一个事件。
type sseEvent struct {
	Type    string         `json:"type"`    // progress / success / error / keepalive
	JobID   string         `json:"job_id,omitempty"`
	Stage   string         `json:"stage,omitempty"`
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// sseHub 是进程内 SSE 广播中心。
// key 是 jobID，value 是订阅该 job 的 channel 集合。
// 每次 Publish 会向所有订阅者的 channel 推送事件；channel 满时丢弃（非阻塞）。
type sseHub struct {
	mu    sync.RWMutex
	subs  map[string]map[chan sseEvent]struct{}
	// 独立的全局订阅者扇出——用来推送管理台消息中心的任务状态通知
	all map[chan sseEvent]struct{}
}

var globalSSEHub = &sseHub{
	subs: make(map[string]map[chan sseEvent]struct{}),
	all:  make(map[chan sseEvent]struct{}),
}

// Subscribe 返回一个 channel（带缓冲 16），用于接收该 jobID 的所有事件。
// 返回的 release 函数必须在不再需要时调用，否则会泄漏 channel。
func (h *sseHub) Subscribe(jobID string) (chan sseEvent, func()) {
	ch := make(chan sseEvent, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[jobID]; !ok {
		h.subs[jobID] = make(map[chan sseEvent]struct{})
	}
	h.subs[jobID][ch] = struct{}{}
	release := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[jobID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, jobID)
			}
		}
		close(ch)
	}
	return ch, release
}

// SubscribeAll 订阅全局事件（如任务状态变更通知），不区分 jobID。
func (h *sseHub) SubscribeAll() (chan sseEvent, func()) {
	ch := make(chan sseEvent, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.all[ch] = struct{}{}
	release := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.all, ch)
		close(ch)
	}
	return ch, release
}

// Publish 向指定 jobID 的所有订阅者广播事件。
func (h *sseHub) Publish(evt sseEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set, ok := h.subs[evt.JobID]
	if !ok {
		return
	}
	for ch := range set {
		select {
		case ch <- evt:
		default:
			// channel 满了，丢弃这个事件——客户端可以通过轮询 detail API 补全
		}
	}
}

// PublishAll 向所有订阅者广播（全局事件如 keepalive）。
func (h *sseHub) PublishAll(evt sseEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.all {
		select {
		case ch <- evt:
		default:
		}
	}
}

// formatSSE 把事件格式化为 SSE 协议的字符串：
//
//	event: <type>\ndata: <json>\n\n
//
// 这是浏览器 EventSource 期望的格式。
func formatSSE(evt sseEvent) string {
	buf := strings.Builder{}
	buf.WriteString("event: ")
	buf.WriteString(evt.Type)
	buf.WriteString("\n")
	data, _ := json.Marshal(evt)
	buf.WriteString("data: ")
	buf.WriteString(string(data))
	buf.WriteString("\n\n")
	return buf.String()
}
