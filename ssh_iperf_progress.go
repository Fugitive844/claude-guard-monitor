package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// New clients stream JSON; older distribution packages emit interval text.
type iperfProgress struct {
	pending      []byte
	current      SpeedResult
	final        *SpeedResult
	update       func(SpeedResult)
	serverOutput bool
	errorMessage string
}

func newIPerfProgress(r SpeedResult, update func(SpeedResult)) *iperfProgress {
	return &iperfProgress{current: r, update: update}
}

func (p *iperfProgress) Write(b []byte) (int, error) {
	n := len(b)
	p.pending = append(p.pending, b...)
	for {
		i := bytes.IndexByte(p.pending, '\n')
		if i < 0 {
			break
		}
		p.line(strings.TrimSpace(string(p.pending[:i])))
		p.pending = p.pending[i+1:]
	}
	if len(p.pending) > 1<<20 {
		return n, errors.New("iperf3 输出过长")
	}
	return n, nil
}

func (p *iperfProgress) interval(sample SpeedSample) {
	end, count, rate := sample.End, sample.Bytes, sample.Mbps
	if end <= p.current.Seconds || count < 0 || rate < 0 {
		return
	}
	p.current.Bytes += count
	p.current.Seconds = end
	p.current.Mbps = float64(p.current.Bytes) * 8 / end / 1e6
	p.current.IntervalMbps = &rate
	p.current.RateBasis = "receiver"
	if p.current.Direction == "upload" {
		p.current.RateBasis = "sender"
	}
	if p.current.SourceHostID == "" {
		sample.Retransmits = nil
	}
	if sample.Retransmits != nil {
		n := *sample.Retransmits
		if p.current.Retransmits != nil {
			n += *p.current.Retransmits
		}
		p.current.Retransmits = &n
	}
	if sample.Packets != nil && sample.LostPackets != nil {
		p.current.Packets += *sample.Packets
		p.current.LostPackets += *sample.LostPackets
		if p.current.Packets > 0 {
			p.current.LostPercent = float64(p.current.LostPackets) * 100 / float64(p.current.Packets)
		}
	}
	if sample.JitterMS != nil {
		p.current.JitterMS = *sample.JitterMS
	}
	p.current.Samples = append(p.current.Samples, sample)
	p.current.Status = "running"
	if p.current.Direction == "upload" {
		p.current.Message = "实时发送 · 最近 1 秒"
	} else {
		p.current.Message = "实时接收 · 最近 1 秒"
	}
	p.update(p.current)
}

var iperfTextInterval = regexp.MustCompile(`^\[\s*(?:SUM|\d+)\]\s+([0-9.]+)-\s*([0-9.]+)\s+sec\s+([0-9.]+)\s+([KMGT]?)Bytes\s+([0-9.]+)\s+Mbits/sec(.*)$`)
var iperfTextLoss = regexp.MustCompile(`([0-9.]+)\s+ms\s+(\d+)\s*/\s*(\d+)\s+\(([0-9.]+)%\)`)
var iperfTextRetrans = regexp.MustCompile(`^\s*(\d+)(?:\s*$|\s+(?:sender|[0-9.]+\s+[KMGT]?Bytes))`)

func iperfNumber(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

func (p *iperfProgress) line(line string) {
	if strings.HasPrefix(line, "{") {
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}
		switch event.Event {
		case "error":
			_ = json.Unmarshal(event.Data, &p.errorMessage)
		case "interval":
			var data struct {
				Sum struct {
					Start       float64  `json:"start"`
					End         float64  `json:"end"`
					Bytes       int64    `json:"bytes"`
					Rate        float64  `json:"bits_per_second"`
					Omitted     bool     `json:"omitted"`
					Retransmits *int64   `json:"retransmits"`
					JitterMS    *float64 `json:"jitter_ms"`
					LostPercent *float64 `json:"lost_percent"`
					LostPackets *int64   `json:"lost_packets"`
					Packets     *int64   `json:"packets"`
				} `json:"sum"`
			}
			if json.Unmarshal(event.Data, &data) == nil {
				if data.Sum.Omitted {
					p.current.Message = "预热中 · 不计入平均值"
					p.update(p.current)
					return
				}
				s := data.Sum
				p.interval(SpeedSample{Start: s.Start, End: s.End, Bytes: s.Bytes, Mbps: s.Rate / 1e6, Retransmits: s.Retransmits, JitterMS: s.JitterMS, LostPercent: s.LostPercent, LostPackets: s.LostPackets, Packets: s.Packets})
			}
		case "end":
			if r, err := parseIPerf(append(append([]byte(`{"end":`), event.Data...), '}'), p.current); err == nil {
				p.final = &r
			}
		}
		return
	}
	if line == "Server output:" {
		p.serverOutput = true
	}
	if p.serverOutput {
		return
	}
	if strings.Contains(strings.ToLower(line), "omitted") {
		p.current.Message = "预热中 · 不计入平均值"
		p.update(p.current)
		return
	}
	m := iperfTextInterval.FindStringSubmatch(line)
	if m == nil {
		return
	}
	parallel := p.current.Protocol == "tcp" && p.current.TestConfig != nil && p.current.TestConfig.Parallel > 1
	if parallel != strings.Contains(strings.SplitN(line, "]", 2)[0], "SUM") {
		return
	}
	end := iperfNumber(m[2])
	count := iperfNumber(m[3])
	for _, unit := range []string{"K", "M", "G", "T"} {
		if m[4] == "" {
			break
		}
		count *= 1024
		if m[4] == unit {
			break
		}
	}
	rate := iperfNumber(m[5])
	if strings.Contains(m[6], "sender") {
		p.current.SenderMbps = &rate
		if p.current.Protocol == "tcp" && (p.current.SourceHostID != "" || p.current.Direction == "download") {
			if retrans := iperfTextRetrans.FindStringSubmatch(m[6]); retrans != nil {
				n := int64(iperfNumber(retrans[1]))
				p.current.Retransmits = &n
			}
		}
	}
	if strings.Contains(m[6], "receiver") && end > 0 {
		r := p.current
		r.Bytes = int64(count)
		r.Seconds = end
		r.Mbps = rate
		r.Status = "complete"
		r.RateBasis = "receiver"
		if loss := iperfTextLoss.FindStringSubmatch(m[6]); loss != nil {
			r.JitterMS = iperfNumber(loss[1])
			r.LostPackets = int64(iperfNumber(loss[2]))
			r.Packets = int64(iperfNumber(loss[3]))
			r.LostPercent = iperfNumber(loss[4])
		}
		p.final = &r
	} else if !strings.Contains(m[6], "sender") {
		sample := SpeedSample{Start: iperfNumber(m[1]), End: end, Bytes: int64(count), Mbps: rate}
		if p.current.Protocol == "udp" {
			if loss := iperfTextLoss.FindStringSubmatch(m[6]); loss != nil {
				jitter, lost := iperfNumber(loss[1]), iperfNumber(loss[4])
				sample.JitterMS = &jitter
				sample.LostPercent = &lost
				lostPackets, packets := int64(iperfNumber(loss[2])), int64(iperfNumber(loss[3]))
				sample.LostPackets, sample.Packets = &lostPackets, &packets
			}
		} else if retrans := iperfTextRetrans.FindStringSubmatch(m[6]); retrans != nil {
			n := int64(iperfNumber(retrans[1]))
			sample.Retransmits = &n
		}
		p.interval(sample)
	}
}

func (p *iperfProgress) result() (SpeedResult, error) {
	if len(p.pending) > 0 {
		p.line(strings.TrimSpace(string(p.pending)))
		p.pending = nil
	}
	if p.final == nil {
		return p.current, errors.New("iperf3 缺少接收端统计")
	}
	r := *p.final
	r.IntervalMbps = nil
	r.Message = ""
	return r, nil
}
