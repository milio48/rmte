package main

import (
	"math"
	"sync"
	"time"
)

// Admin login throttling. Failed logins back off exponentially per client IP
// after a few attempts. Because client IPs can come from proxy headers that a
// direct client can forge, a global cap on failures per minute backs this up
// so password guessing stays slow even when the attacker rotates "IPs".
const (
	adminLoginFreeFailures   = 5
	adminLoginMaxLockout     = 15 * time.Minute
	adminLoginGlobalPerMin   = 30
	adminLoginForgetAfter    = time.Hour
	adminLoginMaxTrackedKeys = 10000
)

type adminLoginState struct {
	failures int
	until    time.Time
	last     time.Time
}

var adminLogin = struct {
	mu          sync.Mutex
	byIP        map[string]*adminLoginState
	globalStart time.Time
	globalFails int
}{byIP: map[string]*adminLoginState{}}

// adminLoginAllowed reports whether ip may try a password now, and if not,
// how long to wait.
func adminLoginAllowed(ip string, now time.Time) (bool, time.Duration) {
	adminLogin.mu.Lock()
	defer adminLogin.mu.Unlock()
	rollGlobalWindowLocked(now)
	if adminLogin.globalFails >= adminLoginGlobalPerMin {
		return false, adminLogin.globalStart.Add(time.Minute).Sub(now)
	}
	if st := adminLogin.byIP[ip]; st != nil && now.Before(st.until) {
		return false, st.until.Sub(now)
	}
	return true, 0
}

func adminLoginFailed(ip string, now time.Time) {
	adminLogin.mu.Lock()
	defer adminLogin.mu.Unlock()
	rollGlobalWindowLocked(now)
	adminLogin.globalFails++

	if len(adminLogin.byIP) >= adminLoginMaxTrackedKeys {
		for k, st := range adminLogin.byIP {
			if now.Sub(st.last) > adminLoginForgetAfter || now.After(st.until) {
				delete(adminLogin.byIP, k)
			}
		}
	}
	st := adminLogin.byIP[ip]
	if st == nil {
		st = &adminLoginState{}
		adminLogin.byIP[ip] = st
	}
	if now.Sub(st.last) > adminLoginForgetAfter {
		st.failures = 0
	}
	st.failures++
	st.last = now
	if over := st.failures - adminLoginFreeFailures; over >= 0 {
		lock := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(over)), float64(adminLoginMaxLockout)))
		st.until = now.Add(lock)
	}
}

// rollGlobalWindowLocked starts a new one-minute window for the global cap.
func rollGlobalWindowLocked(now time.Time) {
	if now.Sub(adminLogin.globalStart) >= time.Minute {
		adminLogin.globalStart, adminLogin.globalFails = now, 0
	}
}

func adminLoginSucceeded(ip string) {
	adminLogin.mu.Lock()
	delete(adminLogin.byIP, ip)
	adminLogin.mu.Unlock()
}

func resetAdminLoginLimiter() {
	adminLogin.mu.Lock()
	adminLogin.byIP = map[string]*adminLoginState{}
	adminLogin.globalStart, adminLogin.globalFails = time.Time{}, 0
	adminLogin.mu.Unlock()
}
