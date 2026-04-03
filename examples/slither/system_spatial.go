package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

const bodySegmentInterval = 2

func setupSpatialHooks(gw *SlitherWorld) mmokit.SpatialHooks {
	var foodCount int
	return mmokit.SpatialHooks{
		PreTick: func() {
			gw.Spatial.ClearTransient()
			foodCount = 0
		},
		OnEntity: func(entity ecs.Entity, entry mmokit.SpatialEntry) {
			if gw.FoodMap.HasAll(entity) {
				foodCount++
			}
			if entry.Layer == LayerSnakeHead && gw.SnakeBodyMap.HasAll(entity) {
				body := gw.SnakeBodyMap.Get(entity)
				for i := bodySegmentInterval; i < body.Length; i += bodySegmentInterval {
					seg := body.GetSegment(i)
					gw.Spatial.InsertTransient(mmokit.SpatialEntry{
						Entity: entity,
						X:      seg.X,
						Y:      seg.Y,
						Radius: gw.Cfg.BodyCollisionRadius,
						Layer:  LayerSnakeBody,
					})
				}
			}
		},
		PostTick: func() {
			gw.FoodCount = foodCount
		},
	}
}
