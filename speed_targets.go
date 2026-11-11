package main

type SpeedTarget struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Upload      bool   `json:"upload"`
	DownloadURL string `json:"-"`
	UploadURL   string `json:"-"`
}

var speedTargets = []SpeedTarget{
	{ID: "cloudflare", Name: "Cloudflare · 自动接入", Upload: true},
	{ID: "hetzner-us", Name: "Hetzner · 美国 Ashburn（下载）", DownloadURL: "https://ash-speed.hetzner.com/1GB.bin"},
	{ID: "hetzner-de", Name: "Hetzner · 德国 Falkenstein（下载）", DownloadURL: "https://fsn1-speed.hetzner.com/1GB.bin"},
	{ID: "ovh-fr", Name: "OVHcloud · 法国 Roubaix", Upload: true, UploadURL: "https://lil1-rbx.speedtest.network.ovh.net/speedtest/upload", DownloadURL: "https://lil1-rbx.speedtest.network.ovh.net/files/1Gb.dat"},
	{ID: "ovh-sg", Name: "OVHcloud · 新加坡", Upload: true, UploadURL: "https://sin1-sgp.speedtest.network.ovh.net/speedtest/upload", DownloadURL: "https://sin1-sgp.speedtest.network.ovh.net/files/1Gb.dat"},
	{ID: "ovh-us", Name: "OVHcloud · 美国 Hillsboro", Upload: true, UploadURL: "https://pdx1-hil.speedtest.network.ovh.net/speedtest/upload", DownloadURL: "https://pdx1-hil.speedtest.network.ovh.net/files/1Gb.dat"},
	{ID: "custom", Name: "自定义 OSS / HTTPS", Upload: true},
}

func speedTarget(id string) (SpeedTarget, bool) {
	for _, t := range speedTargets {
		if t.ID == id {
			return t, true
		}
	}
	return SpeedTarget{}, false
}
