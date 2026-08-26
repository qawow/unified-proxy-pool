package crawlers

import "strings"

// githubRawMirrors returns the same GitHub file via several CDNs. FetchAll
// stops at the first URL that yields proxies, so a dead ghproxy does not
// take the whole source down.
//
// path is "owner/repo/branch/file..." as on raw.githubusercontent.com.
func githubRawMirrors(path string) []string {
	path = strings.TrimPrefix(path, "/")
	raw := "https://raw.githubusercontent.com/" + path
	js := jsdelivrFromGitHubPath(path)
	return []string{
		"https://ghproxy.net/https://raw.githubusercontent.com/" + path,
		raw,
		js,
	}
}

func jsdelivrFromGitHubPath(path string) string {
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 4 {
		return "https://cdn.jsdelivr.net/gh/" + path
	}
	return "https://cdn.jsdelivr.net/gh/" + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + parts[3]
}

// DefaultSources returns the unified, de-duplicated crawler set ported from
// jhao104/proxy_pool, Python3WebSpider/ProxyPool, scylla and haipproxy.
func DefaultSources() []Crawler {
	gh := githubRawMirrors

	list := []Crawler{
		// ---- reliable plain text / raw lists (default enabled) ----
		PlainText("thespeedx-http", gh("TheSpeedX/SOCKS-List/master/http.txt"), "http", false, true),
		PlainText("thespeedx-socks4", gh("TheSpeedX/SOCKS-List/master/socks4.txt"), "socks4", false, true),
		PlainText("thespeedx-socks5", gh("TheSpeedX/SOCKS-List/master/socks5.txt"), "socks5", false, true),
		PlainText("a2u-free-proxy-list", gh("a2u/free-proxy-list/master/free-proxy-list.txt"), "http", false, true),
		PlainText("clarketm-proxy-list", gh("clarketm/proxy-list/master/proxy-list.txt"), "http", false, true),
		PlainText("sunny9577-proxy-scraper", gh("sunny9577/proxy-scraper/master/proxies.txt"), "http", false, true),
		PlainText("jetkai-http", gh("jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt"), "http", false, true),
		PlainText("jetkai-socks4", gh("jetkai/proxy-list/main/online-proxies/txt/proxies-socks4.txt"), "socks4", false, true),
		PlainText("jetkai-socks5", gh("jetkai/proxy-list/main/online-proxies/txt/proxies-socks5.txt"), "socks5", false, true),
		PlainText("monosans-http", gh("monosans/proxy-list/main/proxies/http.txt"), "http", false, true),
		PlainText("monosans-socks4", gh("monosans/proxy-list/main/proxies/socks4.txt"), "socks4", false, true),
		PlainText("monosans-socks5", gh("monosans/proxy-list/main/proxies/socks5.txt"), "socks5", false, true),
		// mmpx12/proxy-list was removed and every URL now 404s. Left disabled
		// rather than deleted so the name is not silently reused: the GitHub API
		// returned 404 for this repo on 2026-08-06 while monosans/proxy-list,
		// TheSpeedX/PROXY-List and vakhov/fresh-proxy-list all returned 200
		// through the same call, so it is the repo that is gone, not the network.
		PlainText("mmpx12-http", gh("mmpx12/proxy-list/master/http.txt"), "http", true, false),
		PlainText("mmpx12-socks4", gh("mmpx12/proxy-list/master/socks4.txt"), "socks4", true, false),
		PlainText("mmpx12-socks5", gh("mmpx12/proxy-list/master/socks5.txt"), "socks5", true, false),
		PlainText("roosterkid-http", gh("roosterkid/openproxylist/main/HTTPS_RAW.txt"), "http", false, true),
		PlainText("roosterkid-socks4", gh("roosterkid/openproxylist/main/SOCKS4_RAW.txt"), "socks4", false, true),
		PlainText("roosterkid-socks5", gh("roosterkid/openproxylist/main/SOCKS5_RAW.txt"), "socks5", false, true),
		PlainText("hookzof-socks5", gh("hookzof/socks5_list/master/proxy.txt"), "socks5", false, true),
		PlainText("proxifly-all", gh("proxifly/free-proxy-list/main/proxies/all/data.txt"), "http", false, true),
		PlainText("rmccurdy", []string{"https://www.rmccurdy.com/scripts/proxy/good.txt"}, "http", true, false),
		PlainText("rudnkh", []string{"https://proxy.rudnkh.me/txt"}, "http", true, false),
		PlainText("pubproxy", []string{"http://pubproxy.com/api/proxy?limit=20&format=txt&type=http"}, "http", true, false),

		// ---- added 2026-08-06, each verified against the live endpoint ----
		// Counts below are unique regex matches at probe time, recorded so a
		// source that quietly dries up can be spotted later.
		// openproxylist: 6270 http / 3061 socks5 — largest of the new set.
		PlainText("openproxylist-http", []string{"https://api.openproxylist.xyz/http.txt"}, "http", false, true),
		PlainText("openproxylist-socks4", []string{"https://api.openproxylist.xyz/socks4.txt"}, "socks4", false, true),
		PlainText("openproxylist-socks5", []string{"https://api.openproxylist.xyz/socks5.txt"}, "socks5", false, true),
		// ErcinDedeoglu: 45828 http / 18946 socks5. Very large, so left on —
		// AddRaw dedupes and Trim caps the pool at MaxRawProxies.
		PlainText("ercin-http", gh("ErcinDedeoglu/proxies/main/proxies/http.txt"), "http", false, true),
		PlainText("ercin-socks4", gh("ErcinDedeoglu/proxies/main/proxies/socks4.txt"), "socks4", false, true),
		PlainText("ercin-socks5", gh("ErcinDedeoglu/proxies/main/proxies/socks5.txt"), "socks5", false, true),
		// proxyspace: 2872 http / 1566 socks5.
		PlainText("proxyspace-http", []string{"https://proxyspace.pro/http.txt"}, "http", false, true),
		PlainText("proxyspace-socks4", []string{"https://proxyspace.pro/socks4.txt"}, "socks4", false, true),
		PlainText("proxyspace-socks5", []string{"https://proxyspace.pro/socks5.txt"}, "socks5", false, true),
		// Anonym0usWork1221: 3250 http / 315 socks5.
		PlainText("anonymouswork-http", gh("Anonym0usWork1221/Free-Proxies/main/proxy_files/http_proxies.txt"), "http", false, true),
		PlainText("anonymouswork-socks4", gh("Anonym0usWork1221/Free-Proxies/main/proxy_files/socks4_proxies.txt"), "socks4", false, true),
		PlainText("anonymouswork-socks5", gh("Anonym0usWork1221/Free-Proxies/main/proxy_files/socks5_proxies.txt"), "socks5", false, true),
		// vakhov: 524 http / 21 socks5 — small but refreshed often.
		PlainText("vakhov-http", gh("vakhov/fresh-proxy-list/master/http.txt"), "http", false, true),
		PlainText("vakhov-socks4", gh("vakhov/fresh-proxy-list/master/socks4.txt"), "socks4", false, true),
		PlainText("vakhov-socks5", gh("vakhov/fresh-proxy-list/master/socks5.txt"), "socks5", false, true),
		// zloi-user/hideip.me: 55 http / 219 socks5.
		PlainText("hideip-http", gh("zloi-user/hideip.me/main/http.txt"), "http", false, true),
		PlainText("hideip-socks4", gh("zloi-user/hideip.me/main/socks4.txt"), "socks4", false, true),
		PlainText("hideip-socks5", gh("zloi-user/hideip.me/main/socks5.txt"), "socks5", false, true),
		// Protocol-split proxifly lists: 576 http / 124 socks5. More precise than
		// the combined all/data.txt, which labels everything http.
		PlainText("proxifly-http", gh("proxifly/free-proxy-list/main/proxies/protocols/http/data.txt"), "http", false, true),
		PlainText("proxifly-socks4", gh("proxifly/free-proxy-list/main/proxies/protocols/socks4/data.txt"), "socks4", false, true),
		PlainText("proxifly-socks5", gh("proxifly/free-proxy-list/main/proxies/protocols/socks5/data.txt"), "socks5", false, true),
		// Pre-checked lists: smaller (243 http / 124 socks5) but higher hit rate.
		PlainText("elliottophellia-http", gh("elliottophellia/proxylist/master/results/http/global/http_checked.txt"), "http", false, true),
		PlainText("elliottophellia-socks5", gh("elliottophellia/proxylist/master/results/socks5/global/socks5_checked.txt"), "socks5", false, true),
		// zaeem20: 123 http / 112 socks5.
		PlainText("zaeem20-http", gh("zaeem20/FREE_PROXIES_LIST/master/http.txt"), "http", false, true),
		PlainText("zaeem20-socks4", gh("zaeem20/FREE_PROXIES_LIST/master/socks4.txt"), "socks4", false, true),
		PlainText("zaeem20-socks5", gh("zaeem20/FREE_PROXIES_LIST/master/socks5.txt"), "socks5", false, true),
		// TuanMinPay: 3315 socks5.
		PlainText("tuanminpay-socks5", gh("TuanMinPay/live-proxy/master/socks5.txt"), "socks5", false, true),
		PlainText("tuanminpay-http", gh("TuanMinPay/live-proxy/master/http.txt"), "http", false, true),
		// hendrikbgr: 684 http.
		PlainText("hendrikbgr-http", gh("hendrikbgr/Free-Proxy-Repo/master/proxy_list.txt"), "http", false, true),
		// casals-ar: 68781 matches / 9MB. Off by default — one round would fill
		// MaxRawProxies on its own and crowd out every other source.
		PlainText("casals-http", gh("casals-ar/proxy-list/main/http"), "http", false, false),
		PlainText("casals-socks5", gh("casals-ar/proxy-list/main/socks5"), "socks5", false, false),

		// ---- found by `go run ./cmd/discover` on 2026-08-06 ----
		// Two numbers per source: total yield, and how much it adds that no
		// other enabled source already provides ("ADDS" in the discover report).
		// ADDS is the number that matters — these lists mirror each other, so a
		// large total can still be worth nothing. Candidates that scored ADDS≈0
		// were rejected and are not listed here; the input list lives in
		// scripts/source-candidates.txt so a re-run can re-check them.
		//
		// SoliSpirit: 123010 proxies, 74116 new. Off by default: it is 30x
		// MaxRawProxies (4000), so one round fills the raw pool by itself and
		// then Trim evicts other sources' proxies on score ties — everything
		// enters at ScoreInit, so ties are the normal case. Enable it only if you
		// raise MaxRawProxies to match.
		PlainText("solispirit-http", gh("SoliSpirit/proxy-list/main/http.txt"), "http", false, false),
		PlainText("solispirit-socks5", gh("SoliSpirit/proxy-list/main/socks5.txt"), "socks5", false, false),
		// B4RC0DE-TM: 3185 http (2936 adds), 1004 socks4 (343), 257 socks5 (92).
		PlainText("b4rc0de-http", gh("B4RC0DE-TM/proxy-list/main/HTTP.txt"), "http", false, true),
		PlainText("b4rc0de-socks4", gh("B4RC0DE-TM/proxy-list/main/SOCKS4.txt"), "socks4", false, true),
		PlainText("b4rc0de-socks5", gh("B4RC0DE-TM/proxy-list/main/SOCKS5.txt"), "socks5", false, true),
		// rdavydov: 554 http (373 adds), 630 socks4 (172), 247 socks5 (85).
		PlainText("rdavydov-http", gh("rdavydov/proxy-list/main/proxies/http.txt"), "http", false, true),
		PlainText("rdavydov-socks4", gh("rdavydov/proxy-list/main/proxies/socks4.txt"), "socks4", false, true),
		PlainText("rdavydov-socks5", gh("rdavydov/proxy-list/main/proxies/socks5.txt"), "socks5", false, true),
		// im-razvan: 268 http (87 adds).
		PlainText("im-razvan-http", gh("im-razvan/proxy_list/main/http.txt"), "http", false, true),
		// prxchk: small (58 http / 32 socks4) but refreshed frequently.
		PlainText("prxchk-http", gh("prxchk/proxy-list/main/http.txt"), "http", false, true),
		PlainText("prxchk-socks4", gh("prxchk/proxy-list/main/socks4.txt"), "socks4", false, true),

		// ---- JSON / API style (jhao / webspider) ----
		//
		// These must be JSONSource, not RegexSource. They publish host and port
		// as separate fields, which the IP:port regex cannot match — probing the
		// live endpoints on 2026-08-06 showed geonode returning 46KB, sunny9577
		// 194KB and monosans 363KB for zero extracted proxies each. All three
		// were default-enabled, so the pool was paying for the fetch and
		// silently discarding every result.
		JSONSource("docip", []string{"https://www.docip.net/data/free.json"}, "http", true, false),
		JSONSource("geonode", []string{
			"https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc",
			"https://proxylist.geonode.com/api/proxy-list?limit=500&page=2&sort_by=lastChecked&sort_type=desc",
		}, "http", true, true),
		JSONSource("roundproxies", []string{
			"https://roundproxies.com/api/get-free-proxies/?limit=100&page=1&sort_by=lastChecked&sort_type=desc",
		}, "http", true, false),
		JSONSource("scdn", []string{"https://proxy.scdn.io/get_proxies.php?protocol=http&count=100"}, "http", true, false),
		JSONSource("fatezero", []string{"http://proxylist.fatezero.org/proxy.list"}, "http", true, false),
		JSONSource("proxifly-json", gh("proxifly/free-proxy-list/main/proxies/all/data.json"), "http", false, true),
		JSONSource("sunny9577-json", gh("sunny9577/proxy-scraper/master/proxies.json"), "http", false, true),
		// Carries its own per-entry protocol, so one URL covers http/socks4/socks5.
		JSONSource("monosans-json", gh("monosans/proxy-list/main/proxies.json"), "http", false, true),

		// ---- HTML table sites (fragile, default off except a few) ----
		HTMLTable("kuaidaili", []string{
			"https://www.kuaidaili.com/free/inha/1/",
			"https://www.kuaidaili.com/free/intr/1/",
		}, 0, 1, true, false),
		HTMLTable("ip89", []string{"https://www.89ip.cn/index_1.html"}, 0, 1, true, false),
		HTMLTable("ip3366", []string{
			"http://www.ip3366.net/free/?stype=1",
			"http://www.ip3366.net/free/?stype=2",
		}, 0, 1, true, false),
		HTMLTable("kxdaili", []string{
			"http://www.kxdaili.com/dailiip.html",
			"http://www.kxdaili.com/dailiip/2/1.html",
		}, 0, 1, true, false),
		HTMLTable("xicidaili", []string{"http://www.xicidaili.com/nn"}, 1, 2, true, false),
		HTMLTable("free-proxy-list", []string{"https://free-proxy-list.net/"}, 0, 1, true, false),
		HTMLTable("sslproxies", []string{"https://www.sslproxies.org/"}, 0, 1, true, false),
		HTMLTable("us-proxy", []string{"https://www.us-proxy.org/"}, 0, 1, true, false),
		HTMLTable("socks-proxy", []string{"https://www.socks-proxy.net/"}, 0, 1, true, false),
		HTMLTable("ipaddress", []string{"https://www.ipaddress.com/proxy-list/"}, 0, 1, true, false),
		HTMLTable("proxynova", []string{"https://www.proxynova.com/proxy-server-list/"}, 0, 1, true, false),
		HTMLTable("data5u", []string{"http://www.data5u.com"}, 0, 1, true, false),
		HTMLTable("ihuan", []string{"https://ip.ihuan.me/"}, 0, 1, true, false),
		HTMLTable("goodips", []string{"https://www.goodips.com/"}, 0, 1, true, false),
		HTMLTable("zdaye", []string{"https://www.zdaye.com/free/"}, 0, 1, true, false),
		HTMLTable("66ip", []string{"http://www.66ip.cn/"}, 0, 1, true, false),
		HTMLTable("iphai", []string{"http://www.iphai.com/free/ng"}, 0, 1, true, false),
		HTMLTable("jiangxianli", []string{"https://ip.jiangxianli.com/"}, 0, 1, true, false),
		HTMLTable("seofangfa", []string{"https://proxy.seofangfa.com/"}, 0, 1, true, false),
		HTMLTable("taiyangdaili", []string{"http://www.taiyanghttp.com/free/page1/"}, 0, 1, true, false),
		HTMLTable("xiladaili", []string{"http://www.xiladaili.com/http/"}, 0, 1, true, false),
		HTMLTable("goubanjia", []string{"http://www.goubanjia.com/"}, 0, 1, true, false),
		HTMLTable("cool-proxy", []string{
			"https://www.cool-proxy.net/proxies/http_proxy_list/country_code:/port:/anonymous:1",
		}, 0, 1, true, false),
		HTMLTable("proxy-list-org", []string{"http://proxy-list.org/english/index.php?p=1"}, 0, 1, true, false),
		HTMLTable("proxylistplus", []string{"https://list.proxylistplus.com/Fresh-HTTP-Proxy-List-1"}, 1, 2, true, false),
		HTMLTable("cn-proxy", []string{"https://cn-proxy.com/"}, 0, 1, true, false),
		HTMLTable("spys-one", []string{"http://spys.one/en/anonymous-proxy-list/"}, 0, 1, true, false),
		HTMLTable("mrhinkydink", []string{"http://www.mrhinkydink.com/proxies.htm"}, 0, 1, true, false),
		HTMLTable("ab57", []string{"http://ab57.ru/downloads/proxyold.txt"}, 0, 0, true, false),
		RegexSource("proxylists-net", []string{"http://www.proxylists.net/http_highanon.txt"}, "http", true, false),
		RegexSource("my-proxy", []string{"https://www.my-proxy.com/free-proxy-list.html"}, "http", true, false),
		RegexSource("atomintersoft", []string{"http://www.atomintersoft.com/proxy_list_domain_com"}, "http", true, false),
		RegexSource("coderbusy", []string{"https://proxy.coderbusy.com/data/proxylist.json"}, "http", true, false),
		RegexSource("proxydb", []string{"http://proxydb.net/?protocol=http&protocol=https"}, "http", true, false),
		RegexSource("gatherproxy", []string{"http://www.gatherproxy.com/proxylist/anonymity/?t=Anonymous"}, "http", true, false),
		RegexSource("freevpnnode", []string{"https://cn.freevpnnode.com/free-proxy/"}, "http", true, false),
		RegexSource("daili66", []string{"http://api.66daili.com/?format=json"}, "http", true, false),
		RegexSource("yqie", []string{"http://www.yqie.com/ipproxy.htm"}, "http", true, false),
		RegexSource("uqidata", []string{"https://ip.uqidata.com/free/list.html"}, "http", true, false),
		RegexSource("xiaoshudaili", []string{"http://www.xsdaili.cn/"}, "http", true, false),
		RegexSource("zhandaye", []string{"https://www.zdaye.com/dayProxy.html"}, "http", true, false),
		RegexSource("xdaili", []string{"https://www.xdaili.cn/ipagent/freeip/getFreeIps?page=1&rows=10"}, "http", true, false),
		RegexSource("mogumiao", []string{"http://www.mogumiao.com/proxy/free/listFreeIp"}, "http", true, false),
		RegexSource("baizhongsou", []string{"http://ip.baizhongsou.com/"}, "http", true, false),
		RegexSource("httpsdaili", []string{"http://www.httpsdaili.com/"}, "http", true, false),
		RegexSource("ip181", []string{"http://www.ip181.com/"}, "http", true, false),
		RegexSource("swei360", []string{"http://www.swei360.com/"}, "http", true, false),
		RegexSource("yundaili", []string{"http://www.ip3366.net/?stype=3"}, "http", true, false),
		RegexSource("nianshao", []string{"http://www.nianshao.me/"}, "http", true, false),
		RegexSource("cnproxy", []string{"https://www.cnproxy.com/proxy1.html"}, "http", true, false),
		RegexSource("free-proxy-cz", []string{"http://free-proxy.cz/en/proxylist/main/1"}, "http", true, false),
		RegexSource("xroxy", []string{"https://www.xroxy.com/proxylist.php?port=&type=All_http"}, "http", true, false),
		RegexSource("proxyhttp-net", []string{"https://proxyhttp.net/"}, "http", true, false),
		RegexSource("http-proxy-provider", []string{"https://proxyhttp.net/free-list/proxy-anonymous-hide-ip-address/"}, "http", true, false),
	}

	return list
}
