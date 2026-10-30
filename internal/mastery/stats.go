package mastery

import (
	"sort"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

// TagStat is accuracy on one quiz tag across quiz attempts and reviews.
type TagStat struct {
	Tag      string  `json:"tag"`
	Correct  int     `json:"correct"`
	Total    int     `json:"total"`
	Accuracy float64 `json:"accuracy"`
}

// WeakTags returns per-tag accuracy over the most recent `recent` answers per tag,
// weakest first, only for tags with at least minN answers.
func WeakTags(p *progress.Progress, idx map[string]*course.Question, recent, minN int) []TagStat {
	type ans struct {
		qid string
		ok  bool
	}
	var all []ans
	// Quiz attempts and reviews, oldest first. Attempts are appended in time order per
	// module; we merge by timestamp so "recent" means recent.
	type timed struct {
		t  int64
		a  ans
		id int
	}
	var ts []timed
	n := 0
	for _, rec := range p.Modules {
		for _, at := range rec.QuizAttempts {
			for _, a := range at.Answers {
				ts = append(ts, timed{at.At.UnixNano(), ans{a.QID, a.Correct}, n})
				n++
			}
		}
	}
	for _, e := range p.Review.Log {
		ts = append(ts, timed{e.At.UnixNano(), ans{e.QID, e.Correct}, n})
		n++
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].t != ts[j].t {
			return ts[i].t < ts[j].t
		}
		return ts[i].id < ts[j].id
	})
	for _, t := range ts {
		all = append(all, t.a)
	}
	byTag := map[string][]bool{}
	for _, a := range all {
		q := idx[a.qid]
		if q == nil {
			continue
		}
		for _, tag := range q.Tags {
			byTag[tag] = append(byTag[tag], a.ok)
		}
	}
	var out []TagStat
	for tag, rs := range byTag {
		if len(rs) > recent {
			rs = rs[len(rs)-recent:]
		}
		if len(rs) < minN {
			continue
		}
		s := TagStat{Tag: tag, Total: len(rs)}
		for _, ok := range rs {
			if ok {
				s.Correct++
			}
		}
		s.Accuracy = float64(s.Correct) / float64(s.Total)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Accuracy != out[j].Accuracy {
			return out[i].Accuracy < out[j].Accuracy
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// EstimateByKind returns accuracy per estimation kind over all attempts.
func EstimateByKind(p *progress.Progress) map[string][2]int {
	out := map[string][2]int{}
	for _, a := range p.Estimate.Attempts {
		v := out[a.Kind]
		v[1]++
		if a.Within {
			v[0]++
		}
		out[a.Kind] = v
	}
	return out
}
