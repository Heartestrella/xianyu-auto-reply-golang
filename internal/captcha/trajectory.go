// Package captcha 实现闲鱼/淘系 Baxia (NC) 滑块验证的本地自动求解。
//
// 本包全部为纯 Go 实现：通过 CDP（Chrome DevTools Protocol）驱动本地
// Chrome/Chromium，注入真人轨迹的鼠标事件完成滑动，最后校验 x5sec
// cookie 是否真正下发。移植自父仓库（Python 版）的
// common/services/captcha/{trajectory,slider_stealth,verification_checker}.py，
// 轨迹参数与控制点保持与父仓库一致。
package captcha

import (
	"math/rand"
	"time"
)

// Point 是轨迹中的一个控制点：相对起点的 X/Y 位移（像素）与相对上一个
// 控制点的延迟（秒）。
type Point struct {
	X     float64
	Y     float64
	Delay time.Duration
}

// trajectoryParams 复刻父仓库 trajectory.py 中真人通过样本的参数。
var trajectoryParams = struct {
	PreferredXProgress []float64
	PreferredYOffsets  []float64
	DurationMin        float64 // 秒
	DurationMax        float64
	OvershootMin       float64
	OvershootMax       float64
}{
	// 真人模式业务优选样本按时间分位后的 X 进度。
	PreferredXProgress: []float64{
		0.0258, 0.0698, 0.1344, 0.1757, 0.2326,
		0.2584, 0.3101, 0.3643, 0.4160, 0.4910,
		0.5504, 0.6486, 0.7623, 0.8372, 0.9302,
		0.9767, 0.9871, 0.9897, 0.9948, 1.0,
	},
	PreferredYOffsets: []float64{
		2.0, 3.0, 5.0, 5.0, 7.0,
		8.0, 10.0, 11.0, 12.0, 14.0,
		16.0, 19.0, 19.0, 19.0, 15.0,
		13.0, 13.0, 13.0, 13.0, 13.0,
	},
	DurationMin:  0.40,
	DurationMax:  0.48,
	OvershootMin: 0.45,
	OvershootMax: 0.55,
}

// GenerateTrajectory 依据目标滑动距离生成一条真人化滑动轨迹。
//
// 关键设计（与父仓库一致）：
//   - 普通滑块的光标会越过轨道终点 45%~55%，滑块本体由页面限制在终点；
//   - 20 个时间分位控制点，复刻真人样本的速度进度；
//   - 总时长 380~550ms（实际参数取 0.40~0.48s，末端自然收速）；
//   - Y 轴为连续弧线，仅叠加很小的相关扰动，避免锯齿。
//
// distance 为目标滑动距离（像素）；返回相对起点的轨迹。
func GenerateTrajectory(distance float64, r *rand.Rand) []Point {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	params := trajectoryParams
	totalDuration := params.DurationMin + r.Float64()*(params.DurationMax-params.DurationMin)

	cursorDistance := distance
	// 真人样本在 258px 轨道上实际移动约 387px；小距离通常是刮刮乐，不越界。
	if distance >= 180.0 {
		cursorDistance += distance * (params.OvershootMin + r.Float64()*(params.OvershootMax-params.OvershootMin))
	}

	progressPoints := params.PreferredXProgress
	totalSteps := len(progressPoints)
	xPoints := make([]float64, 0, totalSteps)
	previousProgress := 0.0
	const minProgressStep = 0.002
	for index, baseProgress := range progressPoints {
		progress := 1.0
		if index != totalSteps-1 {
			remainingPoints := totalSteps - index - 1
			maxProgress := 1.0 - float64(remainingPoints)*minProgressStep
			progress = baseProgress + (r.Float64()*2-1)*0.008
			if progress < previousProgress+minProgressStep {
				progress = previousProgress + minProgressStep
			}
			if progress > maxProgress {
				progress = maxProgress
			}
		}
		previousProgress = progress
		xPoints = append(xPoints, cursorDistance*progress)
	}

	yScale := 0.88 + r.Float64()*(1.12-0.88)
	yPoints := make([]float64, len(params.PreferredYOffsets))
	for i, baseY := range params.PreferredYOffsets {
		yPoints[i] = baseY*yScale + (r.Float64()*2-1)*0.35
	}

	delayWeights := make([]float64, totalSteps)
	var weightTotal float64
	for i := range delayWeights {
		w := 0.9 + r.Float64()*0.2
		delayWeights[i] = w
		weightTotal += w
	}
	trajectory := make([]Point, totalSteps)
	for i := 0; i < totalSteps; i++ {
		seconds := totalDuration * delayWeights[i] / weightTotal
		trajectory[i] = Point{X: xPoints[i], Y: yPoints[i], Delay: time.Duration(seconds * float64(time.Second))}
	}
	// 强制最后一步精确落在光标目标，避免浮点累积误差。
	trajectory[totalSteps-1].X = cursorDistance
	return trajectory
}

// ScratchDistance 计算刮刮乐类型验证码的滑动距离（父仓库取 25%~35% 轨道宽度）。
func ScratchDistance(track float64, r *rand.Rand) float64 {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return track * (0.25 + r.Float64()*(0.35-0.25))
}
