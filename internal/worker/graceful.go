package worker

import (
	"context"
	"time"

	"go-avatar-service/internal/broker"
)

// FinishInFlight даёт сообщению, которое уже обрабатывается, доработать после
// остановки worker'а. Consumer по отмене ctx перестаёт брать новые сообщения,
// а h получает контекст, который отменяется не сразу, а через drain после
// отмены ctx: миниатюры успевают записаться, и сообщение подтверждается.
// Если за drain обработка не закончилась, контекст отменяется, сообщение
// остаётся неподтверждённым и брокер доставит его повторно (дубли гасит Deduplicate).
//
// drain должен укладываться в terminationGracePeriodSeconds пода вместе
// с закрытием соединений и отправкой трейсов.
func FinishInFlight(h broker.Handler, drain time.Duration) broker.Handler {
	return func(ctx context.Context, msg broker.Message) error {
		hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()

		stop := context.AfterFunc(ctx, func() {
			timer := time.NewTimer(drain)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-hctx.Done():
			}
		})
		defer stop()

		return h(hctx, msg)
	}
}
