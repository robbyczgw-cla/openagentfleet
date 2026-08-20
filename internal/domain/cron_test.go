package domain

import (
	"testing"
	"time"
)

func TestNextCronTimeDailyAndMinuteSchedules(t *testing.T) {
	after := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	next, err := NextCronTime("0 9 * * *", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, time.March, 5, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("daily 09:00 = %s, want %s", next, want)
	}

	minute, err := NextCronTime("* * * * *", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if !minute.Equal(after.Add(time.Minute)) {
		t.Fatalf("every minute = %s, want %s", minute, after.Add(time.Minute))
	}

	step, err := NextCronTime("*/15 * * * *", "UTC", after.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wantStep := time.Date(2027, time.March, 4, 11, 15, 0, 0, time.UTC)
	if !step.Equal(wantStep) {
		t.Fatalf("every 15 minutes = %s, want %s", step, wantStep)
	}
}

func TestNextCronTimeWeekdayAndTimeZone(t *testing.T) {
	// Thursday 4 March 2027 11:00 UTC. Next Monday 09:00 Vienna is CET (UTC+1).
	after := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	next, err := NextCronTime("0 9 * * 1", "Europe/Vienna", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, time.March, 8, 8, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("weekly Monday Vienna = %s, want %s", next, want)
	}

	sunday, err := NextCronTime("30 0 * * 0,7", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	wantSunday := time.Date(2027, time.March, 7, 0, 30, 0, 0, time.UTC)
	if !sunday.Equal(wantSunday) {
		t.Fatalf("Sunday 0 and 7 = %s, want %s", sunday, wantSunday)
	}
}

func TestDefaultNextRunAtHeartbeatAndCron(t *testing.T) {
	after := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	heartbeat, err := DefaultNextRunAt(Routine{
		Kind:                     RoutineKindHeartbeat,
		HeartbeatIntervalSeconds: 60,
		TimeZone:                 "UTC",
	}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat.Equal(after.Add(time.Minute)) {
		t.Fatalf("heartbeat = %s, want %s", heartbeat, after.Add(time.Minute))
	}
	cron, err := DefaultNextRunAt(Routine{
		Kind:           RoutineKindCron,
		CronExpression: "0 * * * *",
		TimeZone:       "UTC",
	}, after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, time.March, 4, 12, 0, 0, 0, time.UTC)
	if !cron.Equal(want) {
		t.Fatalf("hourly = %s, want %s", cron, want)
	}
}
