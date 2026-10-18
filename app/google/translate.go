// Package google translates between the [OI]-compatible request/response
// shapes served by BasicRoute and the Google Gemini Interactions API.
package google

import (
	"fmt"
	"strings"
)

// Interface values accepted by TranslateRequest (the inbound path).
const (
	InterfaceChat      = "chat-completions"
	InterfaceResponses = "responses-api"
)

// TranslateRequest converts an inbound [OI]-compatible JSON body into a
// Google Interactions request body. model is the upstream model slug.
func TranslateRequest(body map[string]any, iface, model string) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("google: empty request body")
	}
	if strings.EqualFold(strings.TrimSpace(iface), InterfaceResponses) {
		return translateResponses(body, model), nil
	}
	return translateChat(body, model), nil
}

func translateChat(body map[string]any, model string) map[string]any {
	out := map[string]any{"model": model, "store": false}
	if v, ok := body["stream"].(bool); ok && v {
		out["stream"] = true
	}

	var systemParts []string
	steps := []any{}
	toolNames := map[string]string{}

	for _, mv := range asSlice(body["messages"]) {
		m := asMap(mv)
		if m == nil {
			continue
		}
		switch strings.ToLower(str(m["role"])) {
		case "system", "developer":
			for _, p := range chatContentParts(m["content"]) {
				if pm := asMap(p); pm != nil && str(pm["type"]) == "text" {
					if t := str(pm["text"]); t != "" {
						systemParts = append(systemParts, t)
					}
				}
			}
		case "user":
			if parts := chatContentParts(m["content"]); len(parts) > 0 {
				steps = appendStep(steps, map[string]any{"type": "user_input", "content": parts})
			}
		case "assistant":
			if parts := chatContentParts(m["content"]); len(parts) > 0 {
				steps = appendStep(steps, map[string]any{"type": "model_output", "content": parts})
			}
			for _, tcv := range asSlice(m["tool_calls"]) {
				tc := asMap(tcv)
				fn := asMap(tc["function"])
				if tc == nil || fn == nil {
					continue
				}
				id := str(tc["id"])
				if id == "" {
					id = fmt.Sprintf("call_%d", len(toolNames)+1)
				}
				name := str(fn["name"])
				if name == "" {
					continue
				}
				// function_call tidak dikirim balik: Interactions API menuntut
				// thought signature milik model, yang tidak ada di format [OI].
				// Nama tetap dicatat untuk mencocokkan function_result.
				toolNames[id] = name
			}
		case "tool":
			id := str(m["tool_call_id"])
			name := str(m["name"])
			if name == "" {
				name = toolNames[id]
			}
			parts := chatContentParts(m["content"])
			steps = append(steps, map[string]any{
				"type":    "function_result",
				"name":    name,
				"call_id": id,
				"result":  resultValue(parts),
			})
		}
	}

	if len(systemParts) > 0 {
		out["system_instruction"] = strings.Join(systemParts, "\n\n")
	}
	out["input"] = steps

	applyChatGeneration(out, body)
	applyChatTools(out, body)
	applyResponseFormat(out, body)
	return out
}

func translateResponses(body map[string]any, model string) map[string]any {
	out := map[string]any{"model": model, "store": false}
	if v, ok := body["stream"].(bool); ok && v {
		out["stream"] = true
	}

	var systemParts []string
	if s := str(body["instructions"]); s != "" {
		systemParts = append(systemParts, s)
	} else {
		for _, p := range responsesContentParts(body["instructions"]) {
			if pm := asMap(p); pm != nil && str(pm["type"]) == "text" {
				systemParts = append(systemParts, str(pm["text"]))
			}
		}
	}

	steps := []any{}
	toolNames := map[string]string{}

	switch v := body["input"].(type) {
	case string:
		if v != "" {
			steps = append(steps, map[string]any{"type": "user_input", "content": []any{textPart(v)}})
		}
	case []any:
		for _, iv := range v {
			item := asMap(iv)
			if item == nil {
				continue
			}
			switch strings.ToLower(str(item["type"])) {
			case "message", "":
				role := strings.ToLower(str(item["role"]))
				parts := responsesContentParts(item["content"])
				switch role {
				case "assistant":
					if len(parts) > 0 {
						steps = appendStep(steps, map[string]any{"type": "model_output", "content": parts})
					}
				case "system", "developer":
					for _, p := range parts {
						if pm := asMap(p); pm != nil && str(pm["type"]) == "text" {
							systemParts = append(systemParts, str(pm["text"]))
						}
					}
				default:
					if len(parts) > 0 {
						steps = appendStep(steps, map[string]any{"type": "user_input", "content": parts})
					}
				}
			case "function_call":
				id := str(item["call_id"])
				if id == "" {
					id = str(item["id"])
				}
				name := str(item["name"])
				if name == "" {
					continue
				}
				// Tidak dikirim balik (butuh thought signature model); hanya
				// dicatat untuk mencocokkan function_call_output.
				toolNames[id] = name
			case "function_call_output":
				id := str(item["call_id"])
				parts := responsesContentParts(item["output"])
				steps = append(steps, map[string]any{
					"type":    "function_result",
					"name":    toolNames[id],
					"call_id": id,
					"result":  resultValue(parts),
				})
			default:
				if parts := responsesContentParts([]any{item}); len(parts) > 0 {
					steps = appendStep(steps, map[string]any{"type": "user_input", "content": parts})
				}
			}
		}
	}

	if len(systemParts) > 0 {
		out["system_instruction"] = strings.Join(systemParts, "\n\n")
	}
	out["input"] = steps

	applyResponsesGeneration(out, body)
	applyChatTools(out, body)
	applyResponseFormat(out, body)
	return out
}

func applyChatGeneration(out, body map[string]any) {
	gc := map[string]any{}
	if n, ok := positiveInt(body["max_tokens"]); ok {
		gc["max_output_tokens"] = n
	}
	if n, ok := positiveInt(body["max_completion_tokens"]); ok {
		gc["max_output_tokens"] = n
	}
	if v, ok := body["temperature"]; ok {
		gc["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		gc["top_p"] = v
	}
	if v, ok := body["top_k"]; ok {
		gc["top_k"] = v
	}
	if seq := stringSlice(body["stop"]); len(seq) > 0 {
		gc["stop_sequences"] = seq
	}
	if lvl := thinkingLevel(str(body["reasoning_effort"])); lvl != "" {
		gc["thinking_level"] = lvl
	}
	if len(gc) > 0 {
		out["generation_config"] = gc
	}
}

func applyResponsesGeneration(out, body map[string]any) {
	gc := map[string]any{}
	if n, ok := positiveInt(body["max_output_tokens"]); ok {
		gc["max_output_tokens"] = n
	}
	if n, ok := positiveInt(body["max_tokens"]); ok {
		gc["max_output_tokens"] = n
	}
	if v, ok := body["temperature"]; ok {
		gc["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		gc["top_p"] = v
	}
	if v, ok := body["top_k"]; ok {
		gc["top_k"] = v
	}
	if s := str(body["reasoning_effort"]); s != "" {
		if lvl := thinkingLevel(s); lvl != "" {
			gc["thinking_level"] = lvl
		}
	}
	if r := asMap(body["reasoning"]); r != nil {
		if lvl := thinkingLevel(str(r["effort"])); lvl != "" {
			gc["thinking_level"] = lvl
		}
	}
	if len(gc) > 0 {
		out["generation_config"] = gc
	}
}

// applyResponseFormat menerjemahkan structured output [OI] ke field
// response_format top-level Google (berupa JSON Schema). Interactions API
// tidak mengenal json_object secara khusus, jadi dipetakan ke schema object.
func applyResponseFormat(out, body map[string]any) {
	rf := asMap(body["response_format"])
	if rf == nil {
		if t := asMap(body["text"]); t != nil {
			rf = asMap(t["format"])
		}
	}
	if rf == nil {
		return
	}
	switch strings.ToLower(str(rf["type"])) {
	case "json_object":
		out["response_format"] = map[string]any{"type": "object"}
	case "json_schema":
		schema := rf["schema"]
		if js := asMap(rf["json_schema"]); js != nil && js["schema"] != nil {
			schema = js["schema"]
		}
		if schema != nil {
			out["response_format"] = schema
		}
	}
}

func applyChatTools(out, body map[string]any) {
	if tools := translateTools(body["tools"]); len(tools) > 0 {
		out["tools"] = tools
	}
}

func translateTools(v any) []any {
	out := []any{}
	for _, tv := range asSlice(v) {
		t := asMap(tv)
		if t == nil || strings.ToLower(str(t["type"])) != "function" {
			continue
		}
		fn := asMap(t["function"])
		if fn == nil {
			fn = t // already in Google's flat function shape
		}
		name := str(fn["name"])
		if name == "" {
			continue
		}
		nt := map[string]any{"type": "function", "name": name}
		if d := str(fn["description"]); d != "" {
			nt["description"] = d
		}
		if p := fn["parameters"]; p != nil {
			nt["parameters"] = p
		}
		out = append(out, nt)
	}
	return out
}

// chatContentParts converts [OI] chat message content (string or parts) into
// Google content parts.
func chatContentParts(content any) []any {
	if s, ok := content.(string); ok {
		if s == "" {
			return nil
		}
		return []any{textPart(s)}
	}
	parts := []any{}
	for _, pv := range asSlice(content) {
		p := asMap(pv)
		if p == nil {
			continue
		}
		switch strings.ToLower(str(p["type"])) {
		case "text", "":
			if t := str(p["text"]); t != "" {
				parts = append(parts, textPart(t))
			}
		case "image_url":
			parts = appendMedia(parts, imageURL(p["image_url"]))
		case "video_url":
			parts = appendMedia(parts, mediaFromURL(videoURL(p["video_url"]), "video"))
		case "input_audio":
			audio := asMap(p["input_audio"])
			if audio == nil {
				continue
			}
			format := strings.ToLower(str(audio["format"]))
			mime := "audio/" + format
			switch format {
			case "mp3":
				mime = "audio/mpeg"
			case "wav", "wave":
				mime = "audio/wav"
			}
			parts = append(parts, mediaPart(mime, str(audio["data"]), ""))
		case "file":
			parts = appendMedia(parts, filePart(asMap(p["file"])))
		}
	}
	return parts
}

// responsesContentParts converts Responses API content into Google parts.
func responsesContentParts(content any) []any {
	if s, ok := content.(string); ok {
		if s == "" {
			return nil
		}
		return []any{textPart(s)}
	}
	parts := []any{}
	for _, pv := range asSlice(content) {
		p := asMap(pv)
		if p == nil {
			continue
		}
		switch strings.ToLower(str(p["type"])) {
		case "input_text", "output_text", "text", "":
			if t := str(p["text"]); t != "" {
				parts = append(parts, textPart(t))
			}
		case "input_image", "image_url":
			parts = appendMedia(parts, imageURL(p["image_url"]))
		case "input_audio":
			audio := asMap(p["input_audio"])
			if audio == nil {
				continue
			}
			format := strings.ToLower(str(audio["format"]))
			mime := "audio/" + format
			if format == "mp3" {
				mime = "audio/mpeg"
			}
			parts = append(parts, mediaPart(mime, str(audio["data"]), ""))
		case "input_file", "file":
			file := p
			if nested := asMap(p["file"]); nested != nil {
				file = nested
			}
			parts = appendMedia(parts, filePart(file))
		case "input_video", "video_url":
			parts = appendMedia(parts, mediaFromURL(videoURL(p["video_url"]), "video"))
		}
	}
	return parts
}

func imageURL(v any) map[string]any {
	if s, ok := v.(string); ok {
		return mediaFromURL(s, "image")
	}
	m := asMap(v)
	if m == nil {
		return nil
	}
	return mediaFromURL(str(m["url"]), "image")
}

func videoURL(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if m := asMap(v); m != nil {
		return str(m["url"])
	}
	return ""
}

func filePart(file map[string]any) map[string]any {
	if file == nil {
		return nil
	}
	if fd := str(file["file_data"]); fd != "" {
		if mime, data, ok := parseDataURL(fd); ok {
			return mediaPart(mime, data, "")
		}
		return mediaPart(str(file["mime_type"]), fd, "")
	}
	if id := str(file["file_id"]); id != "" {
		return mediaPart(str(file["mime_type"]), "", id)
	}
	if uri := str(file["file_uri"]); uri != "" {
		return mediaPart(str(file["mime_type"]), "", uri)
	}
	return nil
}

func mediaFromURL(raw, fallback string) map[string]any {
	if raw == "" {
		return nil
	}
	if mime, data, ok := parseDataURL(raw); ok {
		return mediaPart(mime, data, "")
	}
	mime := ""
	if fallback != "" {
		mime = fallback + "/*"
	}
	return mediaPart(mime, "", raw)
}

func mediaPart(mime, data, uri string) map[string]any {
	typ := contentTypeForMime(mime, uri)
	part := map[string]any{"type": typ}
	if data != "" {
		part["data"] = data
	}
	if uri != "" {
		part["uri"] = uri
	}
	if mime != "" && !strings.HasSuffix(mime, "/*") {
		part["mime_type"] = mime
	}
	return part
}

func contentTypeForMime(mime, uri string) string {
	m := strings.ToLower(mime)
	if m == "" && uri != "" {
		switch {
		case strings.Contains(uri, ".png"), strings.Contains(uri, ".jpg"), strings.Contains(uri, ".jpeg"),
			strings.Contains(uri, ".webp"), strings.Contains(uri, ".gif"):
			return "image"
		case strings.Contains(uri, ".mp4"), strings.Contains(uri, ".mov"), strings.Contains(uri, ".webm"):
			return "video"
		case strings.Contains(uri, ".mp3"), strings.Contains(uri, ".wav"), strings.Contains(uri, ".ogg"):
			return "audio"
		case strings.Contains(uri, ".pdf"):
			return "document"
		}
	}
	switch {
	case strings.HasPrefix(m, "image/"):
		return "image"
	case strings.HasPrefix(m, "audio/"):
		return "audio"
	case strings.HasPrefix(m, "video/"):
		return "video"
	default:
		return "document"
	}
}

func parseDataURL(s string) (mime, data string, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", "", false
	}
	rest := s[len("data:"):]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	mime = meta
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	if data == "" {
		return "", "", false
	}
	return mime, data, true
}

func appendMedia(parts []any, p map[string]any) []any {
	if p == nil {
		return parts
	}
	return append(parts, p)
}

func appendStep(steps []any, step map[string]any) []any {
	if len(steps) > 0 {
		last := asMap(steps[len(steps)-1])
		typ := str(step["type"])
		if last != nil && str(last["type"]) == typ && (typ == "user_input" || typ == "model_output") {
			last["content"] = append(asSlice(last["content"]), asSlice(step["content"])...)
			return steps
		}
	}
	return append(steps, step)
}

func textPart(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

// resultValue mengubah parts hasil tool menjadi nilai field `result` Google:
// string bila murni teks (multimodal tak didukung sebagian model), atau
// daftar part bila mengandung media.
func resultValue(parts []any) any {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		pm := asMap(p)
		if pm == nil || str(pm["type"]) != "text" {
			return parts
		}
		texts = append(texts, str(pm["text"]))
	}
	return strings.Join(texts, "\n")
}

// thinkingLevel memetakan reasoning_effort [OI] ke Google thinking_level.
// Nilai kosong/auto dibiarkan (model memutuskan). Catatan: sebagian model
// hanya menerima "low"/"high".
func thinkingLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal", "none", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	default:
		return ""
	}
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func positiveInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int64(n), true
		}
	case int:
		if n > 0 {
			return int64(n), true
		}
	case int64:
		if n > 0 {
			return n, true
		}
	}
	return 0, false
}

func stringSlice(v any) []string {
	switch s := v.(type) {
	case string:
		if s != "" {
			return []string{s}
		}
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if es := str(e); es != "" {
				out = append(out, es)
			}
		}
		return out
	}
	return nil
}
