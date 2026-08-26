package web

import (
	"net/http"
)

// handlePublicDebug is the LAN debug snapshot: pool size, 7892/7893, mihomo
// probe/prod liveness and last unexpected exit. No secrets.
func (a *App) handlePublicDebug(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"ok":      true,
		"service": "unified-proxy-pool",
		"lan":     true,
	}
	if a.free != nil {
		if total, validated, raw, err := a.free.Store().Count(r.Context()); err == nil {
			data["total"] = total
			data["validated"] = validated
			data["raw"] = raw
			data["backend"] = a.free.Store().Backend()
		}
	}
	if a.direct != nil {
		st := a.direct.Status()
		data["direct_proxy"] = st.Running
		data["chain_proxy"] = st.ChainRunning
		data["direct_addr"] = st.ListenAddr
		data["chain_addr"] = st.ChainListenAddr
	}
	if a.mihomo != nil {
		ms := a.mihomo.Status()
		data["mihomo"] = map[string]any{
			"binary_available": ms.BinaryAvailable,
			"prod_running":     ms.ProdRunning,
			"probe_running":    ms.ProbeRunning,
			"prod_pid":         ms.ProdPID,
			"probe_pid":        ms.ProbePID,
			"last_prod_exit":   ms.LastProdExit,
			"last_probe_exit":  ms.LastProbeExit,
			"last_prod_exit_at":  ms.LastProdExitAt,
			"last_probe_exit_at": ms.LastProbeExitAt,
		}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: data})
}

func (a *App) handlePublicDebugMihomo(w http.ResponseWriter, r *http.Request) {
	if a.mihomo == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"available": false}})
		return
	}
	st := a.mihomo.Status()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"binary_available":   st.BinaryAvailable,
		"prod_running":       st.ProdRunning,
		"probe_running":      st.ProbeRunning,
		"prod_pid":           st.ProdPID,
		"probe_pid":          st.ProbePID,
		"last_prod_exit":     st.LastProdExit,
		"last_probe_exit":    st.LastProbeExit,
		"last_prod_exit_at":  st.LastProdExitAt,
		"last_probe_exit_at": st.LastProbeExitAt,
		"host_os":            st.HostOS,
		"host_arch":          st.HostArch,
	}})
}
