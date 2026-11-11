package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"time"
)

type Event struct {
	ID      int64  `json:"id"`
	First   int64  `json:"first"`
	Last    int64  `json:"last"`
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	SPort   int    `json:"sport"`
	DPort   int    `json:"dport"`
	Proto   string `json:"proto"`
	Family  int    `json:"family"`
	Packets int64  `json:"packets"`
	Bytes   int64  `json:"bytes"`
	Domains string `json:"domains"`
	Reason  string `json:"reason"`
	Source  string `json:"source"`
}
type Store struct {
	*sql.DB
	path string
}

func openStore(path string) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA busy_timeout=3000", "PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA wal_autocheckpoint=256", "PRAGMA journal_size_limit=4194304", `CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT, first INTEGER NOT NULL,last INTEGER NOT NULL,src TEXT NOT NULL,dst TEXT NOT NULL,sport INTEGER,dport INTEGER,proto TEXT,family INTEGER,packets INTEGER,bytes INTEGER,domains TEXT,reason TEXT,source TEXT)`, `CREATE INDEX IF NOT EXISTS event_time ON events(last)`, `CREATE INDEX IF NOT EXISTS event_dst ON events(dst,last)`, `CREATE TABLE IF NOT EXISTS samples(id INTEGER PRIMARY KEY,ts INTEGER,epoch TEXT,p4 INTEGER,p6 INTEGER,b4 INTEGER,b6 INTEGER,d4 INTEGER,d6 INTEGER,received INTEGER,lost INTEGER)`, `CREATE INDEX IF NOT EXISTS sample_time ON samples(ts)`, `CREATE TABLE IF NOT EXISTS lifecycle(id INTEGER PRIMARY KEY,ts INTEGER,kind TEXT,message TEXT)`, `CREATE INDEX IF NOT EXISTS lifecycle_time ON lifecycle(ts)`, `CREATE TABLE IF NOT EXISTS speed_results(id INTEGER PRIMARY KEY,ts INTEGER NOT NULL,result TEXT NOT NULL)`, `CREATE INDEX IF NOT EXISTS speed_time ON speed_results(ts)`, `CREATE INDEX IF NOT EXISTS speed_node_direction ON speed_results(json_extract(result,'$.node_id'),json_extract(result,'$.direction'),id)`} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	return &Store{db, path}, nil
}
func (s *Store) insertBatch(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, e := s.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	stmt, e := tx.Prepare(`INSERT INTO events(first,last,src,dst,sport,dport,proto,family,packets,bytes,domains,reason,source) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if e != nil {
		return e
	}
	defer stmt.Close()
	for _, v := range events {
		if _, e = stmt.Exec(v.First, v.Last, v.Src, v.Dst, v.SPort, v.DPort, v.Proto, v.Family, v.Packets, v.Bytes, v.Domains, v.Reason, v.Source); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) events(q string, before int64, limit int) ([]Event, error) {
	return s.eventsRange(q, before, limit, 0, 1<<62)
}
func (s *Store) eventsRange(q string, before int64, limit int, from, to int64) ([]Event, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	if before == 0 {
		before = 1 << 62
	}
	rows, e := s.Query(`SELECT id,first,last,src,dst,sport,dport,proto,family,packets,bytes,domains,reason,source FROM events WHERE id<? AND last>=? AND last<? AND (?='' OR instr(src,?)>0 OR instr(dst,?)>0 OR instr(domains,?)>0 OR instr(proto,?)>0) ORDER BY id DESC LIMIT ?`, before, from, to, q, q, q, q, q, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	r := []Event{}
	for rows.Next() {
		var v Event
		if e = rows.Scan(&v.ID, &v.First, &v.Last, &v.Src, &v.Dst, &v.SPort, &v.DPort, &v.Proto, &v.Family, &v.Packets, &v.Bytes, &v.Domains, &v.Reason, &v.Source); e != nil {
			return nil, e
		}
		r = append(r, v)
	}
	return r, rows.Err()
}
func (s *Store) timeline(from, to, before int64) ([]map[string]any, error) {
	r := []map[string]any{}
	if before == 0 {
		before = 1 << 62
	}
	rows, e := s.Query("SELECT id,ts,kind,message FROM lifecycle WHERE ts>=? AND ts<? AND id<? ORDER BY id DESC LIMIT 51", from, to, before)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, ts int64
		var k, m string
		if e = rows.Scan(&id, &ts, &k, &m); e != nil {
			return nil, e
		}
		r = append(r, map[string]any{"id": id, "time": ts, "kind": k, "message": m})
	}
	return r, rows.Err()
}
func (s *Store) size() int64 {
	var n int64
	for _, p := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if f, e := os.Stat(p); e == nil {
			n += f.Size()
		}
	}
	return n
}
func (s *Store) prune(c Config) error {
	now := time.Now().UnixMilli()
	queries := []struct {
		q string
		v any
	}{{"DELETE FROM speed_results WHERE ts<?", now - int64(c.StatsDays)*86400000}, {"DELETE FROM speed_results WHERE id < COALESCE((SELECT id FROM speed_results ORDER BY id DESC LIMIT 1 OFFSET 10000),0)", nil}, {"DELETE FROM events WHERE last<?", now - int64(c.DetailDays)*86400000}, {"DELETE FROM samples WHERE ts<?", now - int64(c.StatsDays)*86400000}, {"DELETE FROM lifecycle WHERE ts<?", now - int64(c.StatsDays)*86400000}, {"DELETE FROM events WHERE id < COALESCE((SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET 150000),0)", nil}, {"DELETE FROM samples WHERE id < COALESCE((SELECT id FROM samples ORDER BY id DESC LIMIT 1 OFFSET 180000),0)", nil}, {"DELETE FROM lifecycle WHERE id < COALESCE((SELECT id FROM lifecycle ORDER BY id DESC LIMIT 1 OFFSET 10000),0)", nil}}
	for _, q := range queries {
		var e error
		if q.v == nil {
			_, e = s.Exec(q.q)
		} else {
			_, e = s.Exec(q.q, q.v)
		}
		if e != nil {
			return e
		}
	}
	if s.size() > c.MaxDBMB*1024*1024*8/10 {
		for _, table := range []string{"events", "samples", "lifecycle", "speed_results"} {
			if _, e := s.Exec("DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table + " ORDER BY id LIMIT (SELECT count(*)/4 FROM " + table + "))"); e != nil {
				return e
			}
		}
	}
	if _, e := s.Exec("PRAGMA incremental_vacuum(2048)"); e != nil {
		return e
	}
	_, e := s.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return e
}
func (a *App) audit(k, m string) {
	if len(m) > 2000 {
		m = m[:2000]
	}
	if _, e := a.db.Exec("INSERT INTO lifecycle(ts,kind,message) VALUES(?,?,?)", time.Now().UnixMilli(), k, m); e != nil {
		a.mu.Lock()
		a.lastError = "事件记录失败: " + e.Error()
		a.mu.Unlock()
	}
}
func (a *App) writer() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	batch := map[string]Event{}
	flush := func() {
		v := make([]Event, 0, len(batch))
		var count uint64
		for _, e := range batch {
			if e.Domains == "" && !a.demo {
				e.Domains = a.domains(e.Dst)
			}
			v = append(v, e)
			count += uint64(e.Packets)
		}
		batch = map[string]Event{}
		if a.db.size() > a.cfg.MaxDBMB*1024*1024 {
			a.mu.Lock()
			a.writerLost += count
			a.lastError = "数据库达到容量限制，暂停详细记录；阻断继续"
			a.mu.Unlock()
			return
		}
		if e := a.db.insertBatch(v); e != nil {
			a.mu.Lock()
			a.writerLost += count
			a.lastError = "SQLite 写入失败: " + e.Error()
			a.mu.Unlock()
		}
	}
	for {
		select {
		case <-a.ctx.Done():
			flush()
			return
		case e := <-a.events:
			key := fmt.Sprintf("%s|%s|%d|%d|%s|%s", e.Src, e.Dst, e.SPort, e.DPort, e.Proto, e.Source)
			if old, ok := batch[key]; ok {
				old.Last = e.Last
				old.Packets += e.Packets
				old.Bytes += e.Bytes
				batch[key] = old
			} else {
				batch[key] = e
			}
		case <-tick.C:
			flush()
		}
	}
}
func (a *App) enqueue(e Event) {
	if e.First == 0 {
		e.First = time.Now().UnixMilli()
	}
	e.Last = e.First
	e.Packets = 1
	a.mu.Lock()
	a.received++
	a.mu.Unlock()
	select {
	case a.events <- e:
	default:
		a.mu.Lock()
		a.lost++
		a.mu.Unlock()
	}
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
