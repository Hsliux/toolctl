package modules

import (
	"toolctl/internal/core"
	"toolctl/internal/modules/assess"
	"toolctl/internal/modules/batch"
	"toolctl/internal/modules/bench"
	"toolctl/internal/modules/doctor"
	"toolctl/internal/modules/mysql"
	"toolctl/internal/modules/perf"
	"toolctl/internal/modules/raid"
	"toolctl/internal/modules/redis"
	"toolctl/internal/modules/system"
)

func Builtins() []core.Module {
	assessmentSources := []core.Module{system.New(), perf.New(), raid.New(), batch.New(), bench.New()}
	sources := append(append([]core.Module(nil), assessmentSources...), mysql.New(), redis.New())
	return append(sources, doctor.New(sources), assess.New(assessmentSources))
}
