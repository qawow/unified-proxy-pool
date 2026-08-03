package crawlers

// DefaultSources returns the unified, de-duplicated crawler set ported from
// jhao104/proxy_pool, Python3WebSpider/ProxyPool, scylla and haipproxy.
func DefaultSources() []Crawler {
	gh := func(path string) string {
		return "https://gh.awa91.cyou/https://raw.githubusercontent.com/" + path
	}

	list := []Crawler{
		// ---- reliable plain text / raw lists (default enabled) ----
		PlainText("thespeedx-http", []string{gh("TheSpeedX/SOCKS-List/master/http.txt")}, "http", false, true),
		PlainText("thespeedx-socks4", []string{gh("TheSpeedX/SOCKS-List/master/socks4.txt")}, "socks4", false, true),
		PlainText("thespeedx-socks5", []string{gh("TheSpeedX/SOCKS-List/master/socks5.txt")}, "socks5", false, true),
		PlainText("a2u-free-proxy-list", []string{gh("a2u/free-proxy-list/master/free-proxy-list.txt")}, "http", false, true),
		PlainText("clarketm-proxy-list", []string{gh("clarketm/proxy-list/master/proxy-list.txt")}, "http", false, true),
		PlainText("sunny9577-proxy-scraper", []string{gh("sunny9577/proxy-scraper/master/proxies.txt")}, "http", false, true),
		PlainText("jetkai-http", []string{gh("jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt")}, "http", false, true),
		PlainText("jetkai-socks4", []string{gh("jetkai/proxy-list/main/online-proxies/txt/proxies-socks4.txt")}, "socks4", false, true),
		PlainText("jetkai-socks5", []string{gh("jetkai/proxy-list/main/online-proxies/txt/proxies-socks5.txt")}, "socks5", false, true),
		PlainText("monosans-http", []string{gh("monosans/proxy-list/main/proxies/http.txt")}, "http", false, true),
		PlainText("monosans-socks4", []string{gh("monosans/proxy-list/main/proxies/socks4.txt")}, "socks4", false, true),
		PlainText("monosans-socks5", []string{gh("monosans/proxy-list/main/proxies/socks5.txt")}, "socks5", false, true),
		PlainText("mmpx12-http", []string{gh("mmpx12/proxy-list/master/http.txt")}, "http", false, true),
		PlainText("mmpx12-socks4", []string{gh("mmpx12/proxy-list/master/socks4.txt")}, "socks4", false, true),
		PlainText("mmpx12-socks5", []string{gh("mmpx12/proxy-list/master/socks5.txt")}, "socks5", false, true),
		PlainText("roosterkid-http", []string{gh("roosterkid/openproxylist/main/HTTPS_RAW.txt")}, "http", false, true),
		PlainText("roosterkid-socks4", []string{gh("roosterkid/openproxylist/main/SOCKS4_RAW.txt")}, "socks4", false, true),
		PlainText("roosterkid-socks5", []string{gh("roosterkid/openproxylist/main/SOCKS5_RAW.txt")}, "socks5", false, true),
		PlainText("hookzof-socks5", []string{gh("hookzof/socks5_list/master/proxy.txt")}, "socks5", false, true),
		PlainText("proxifly-all", []string{"https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/all/data.txt"}, "http", false, true),
		PlainText("rmccurdy", []string{"https://www.rmccurdy.com/scripts/proxy/good.txt"}, "http", true, false),
		PlainText("rudnkh", []string{"https://proxy.rudnkh.me/txt"}, "http", true, false),
		PlainText("pubproxy", []string{"http://pubproxy.com/api/proxy?limit=20&format=txt&type=http"}, "http", true, false),

		// ---- JSON / API style (jhao / webspider) ----
		RegexSource("docip", []string{"https://www.docip.net/data/free.json"}, "http", true, true),
		RegexSource("geonode", []string{
			"https://proxylist.geonode.com/api/proxy-list?limit=100&page=1&sort_by=lastChecked&sort_type=desc",
		}, "http", true, true),
		RegexSource("roundproxies", []string{
			"https://roundproxies.com/api/get-free-proxies/?limit=100&page=1&sort_by=lastChecked&sort_type=desc",
		}, "http", true, false),
		RegexSource("scdn", []string{"https://proxy.scdn.io/get_proxies.php?protocol=http&count=100"}, "http", true, false),
		RegexSource("fatezero", []string{"http://proxylist.fatezero.org/proxy.list"}, "http", true, true),
		RegexSource("proxifly-json", []string{
			"https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/all/data.json",
		}, "http", false, true),
		RegexSource("sunny9577-json", []string{gh("sunny9577/proxy-scraper/master/proxies.json")}, "http", false, true),

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
		HTMLTable("free-proxy-list", []string{"https://free-proxy-list.net/"}, 0, 1, true, true),
		HTMLTable("sslproxies", []string{"https://www.sslproxies.org/"}, 0, 1, true, true),
		HTMLTable("us-proxy", []string{"https://www.us-proxy.org/"}, 0, 1, true, true),
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
		RegexSource("comp0", []string{"https://proxy.rudnkh.me/txt"}, "http", true, false),
		RegexSource("http-proxy-provider", []string{"https://proxyhttp.net/free-list/proxy-anonymous-hide-ip-address/"}, "http", true, false),
	}

	return list
}
