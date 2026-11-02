package rustplus

import "rustwatch/internal/pb"

// Marker types (AppMarkerType).
const (
	MarkerPlayer           = 1
	MarkerExplosion        = 2
	MarkerVendingMachine   = 3
	MarkerCH47             = 4
	MarkerCargoShip        = 5
	MarkerCrate            = 6
	MarkerGenericRadius    = 7
	MarkerPatrolHelicopter = 8
	MarkerTravelingVendor  = 9
)

// Entity types (AppEntityType).
const (
	EntitySwitch         = 1
	EntityAlarm          = 2
	EntityStorageMonitor = 3
)

type Info struct {
	Name          string `json:"name"`
	HeaderImage   string `json:"headerImage"`
	URL           string `json:"url"`
	Map           string `json:"map"`
	MapSize       uint32 `json:"mapSize"`
	WipeTime      uint32 `json:"wipeTime"`
	Players       uint32 `json:"players"`
	MaxPlayers    uint32 `json:"maxPlayers"`
	QueuedPlayers uint32 `json:"queuedPlayers"`
	Seed          uint32 `json:"seed"`
	Salt          uint32 `json:"salt"`
	LogoImage     string `json:"logoImage"`
}

type Time struct {
	DayLengthMinutes float32 `json:"dayLengthMinutes"`
	TimeScale        float32 `json:"timeScale"`
	Sunrise          float32 `json:"sunrise"`
	Sunset           float32 `json:"sunset"`
	Time             float32 `json:"time"`
}

type Monument struct {
	Token string  `json:"token"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
}

type MapData struct {
	Width       uint32     `json:"width"`
	Height      uint32     `json:"height"`
	JPG         []byte     `json:"-"`
	OceanMargin int32      `json:"oceanMargin"`
	Monuments   []Monument `json:"monuments"`
	Background  string     `json:"background"`
}

type Member struct {
	SteamID   uint64  `json:"steamId,string"`
	Name      string  `json:"name"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Online    bool    `json:"isOnline"`
	SpawnTime uint32  `json:"spawnTime"`
	Alive     bool    `json:"isAlive"`
	DeathTime uint32  `json:"deathTime"`
}

type Note struct {
	Type   int32   `json:"type"` // 0 = death marker, 1 = map pin
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Icon   int32   `json:"icon"`
	Colour int32   `json:"colour"`
	Label  string  `json:"label"`
	Leader bool    `json:"leader"`
}

type TeamInfo struct {
	LeaderSteamID uint64   `json:"leaderSteamId,string"`
	Members       []Member `json:"members"`
	MapNotes      []Note   `json:"mapNotes"`
	LeaderNotes   []Note   `json:"leaderMapNotes"`
}

type TeamMessage struct {
	SteamID uint64 `json:"steamId,string"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Color   string `json:"color"`
	Time    uint32 `json:"time"`
}

type Item struct {
	ItemID      int32 `json:"itemId"`
	Quantity    int32 `json:"quantity"`
	IsBlueprint bool  `json:"isBlueprint"`
}

type Payload struct {
	HasValue         bool   `json:"-"`
	Value            bool   `json:"value"`
	Items            []Item `json:"items"`
	Capacity         int32  `json:"capacity"`
	HasProtection    bool   `json:"hasProtection"`
	ProtectionExpiry uint32 `json:"protectionExpiry"`
}

type EntityInfo struct {
	Type    int32   `json:"type"`
	Payload Payload `json:"payload"`
}

type SellOrder struct {
	ItemID              int32   `json:"itemId"`
	Quantity            int32   `json:"quantity"`
	CurrencyID          int32   `json:"currencyId"`
	CostPerItem         int32   `json:"costPerItem"`
	AmountInStock       int32   `json:"amountInStock"`
	ItemIsBlueprint     bool    `json:"itemIsBlueprint"`
	CurrencyIsBlueprint bool    `json:"currencyIsBlueprint"`
	ItemCondition       float32 `json:"itemCondition"`
	ItemConditionMax    float32 `json:"itemConditionMax"`
}

type Marker struct {
	ID         uint32      `json:"id"`
	Type       int32       `json:"type"`
	X          float32     `json:"x"`
	Y          float32     `json:"y"`
	SteamID    uint64      `json:"steamId,string"`
	Rotation   float32     `json:"rotation"`
	Radius     float32     `json:"radius"`
	Name       string      `json:"name"`
	Color1     [4]float32  `json:"color1"`
	Alpha      float32     `json:"alpha"`
	OutOfStock bool        `json:"outOfStock"`
	SellOrders []SellOrder `json:"sellOrders"`
}

type Response struct {
	Raw        []byte // the whole AppMessage, for diagnostics
	Seq        uint32
	Success    bool
	Error      string
	Info       *Info
	Time       *Time
	Map        *MapData
	TeamInfo   *TeamInfo
	TeamChat   []TeamMessage
	HasChat    bool
	EntityInfo *EntityInfo
	Flag       *bool
	Markers    []Marker
	HasMarkers bool
}

type EntityChanged struct {
	EntityID uint32
	Payload  Payload
}

type Broadcast struct {
	TeamChanged   *TeamInfo
	TeamMessage   *TeamMessage
	EntityChanged *EntityChanged
}

// ---- decoding ----

func decodeInfo(b []byte) *Info {
	i := &Info{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			i.Name = f.Str()
		case 2:
			i.HeaderImage = f.Str()
		case 3:
			i.URL = f.Str()
		case 4:
			i.Map = f.Str()
		case 5:
			i.MapSize = uint32(f.V)
		case 6:
			i.WipeTime = uint32(f.V)
		case 7:
			i.Players = uint32(f.V)
		case 8:
			i.MaxPlayers = uint32(f.V)
		case 9:
			i.QueuedPlayers = uint32(f.V)
		case 10:
			i.Seed = uint32(f.V)
		case 11:
			i.Salt = uint32(f.V)
		case 12:
			i.LogoImage = f.Str()
		}
	})
	return i
}

func decodeTime(b []byte) *Time {
	t := &Time{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			t.DayLengthMinutes = f.Float()
		case 2:
			t.TimeScale = f.Float()
		case 3:
			t.Sunrise = f.Float()
		case 4:
			t.Sunset = f.Float()
		case 5:
			t.Time = f.Float()
		}
	})
	return t
}

func decodeMap(b []byte) *MapData {
	m := &MapData{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			m.Width = uint32(f.V)
		case 2:
			m.Height = uint32(f.V)
		case 3:
			m.JPG = append([]byte(nil), f.B...)
		case 4:
			m.OceanMargin = f.Int32()
		case 5:
			mon := Monument{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					mon.Token = g.Str()
				case 2:
					mon.X = g.Float()
				case 3:
					mon.Y = g.Float()
				}
			})
			m.Monuments = append(m.Monuments, mon)
		case 6:
			m.Background = f.Str()
		}
	})
	return m
}

func decodeTeamInfo(b []byte) *TeamInfo {
	t := &TeamInfo{Members: []Member{}}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			t.LeaderSteamID = f.V
		case 2:
			m := Member{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					m.SteamID = g.V
				case 2:
					m.Name = g.Str()
				case 3:
					m.X = g.Float()
				case 4:
					m.Y = g.Float()
				case 5:
					m.Online = g.Bool()
				case 6:
					m.SpawnTime = uint32(g.V)
				case 7:
					m.Alive = g.Bool()
				case 8:
					m.DeathTime = uint32(g.V)
				}
			})
			t.Members = append(t.Members, m)
		case 3, 4:
			n := Note{Leader: f.Num == 4}
			pb.Each(f.B, func(g pb.Field) {
				switch {
				case g.Num == 2 && g.Wire == pb.WireVarint:
					n.Type = g.Int32()
				case g.Num == 3 && g.Wire == pb.WireFixed32:
					n.X = g.Float()
				case g.Num == 4 && g.Wire == pb.WireFixed32:
					n.Y = g.Float()
				case g.Num == 5 && g.Wire == pb.WireVarint:
					n.Icon = g.Int32()
				case g.Num == 6 && g.Wire == pb.WireVarint:
					n.Colour = g.Int32()
				case g.Num == 7 && g.Wire == pb.WireBytes:
					n.Label = g.Str()
				}
			})
			if f.Num == 4 {
				t.LeaderNotes = append(t.LeaderNotes, n)
			} else {
				t.MapNotes = append(t.MapNotes, n)
			}
		}
	})
	return t
}

func decodeTeamMessage(b []byte) TeamMessage {
	m := TeamMessage{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			m.SteamID = f.V
		case 2:
			m.Name = f.Str()
		case 3:
			m.Message = f.Str()
		case 4:
			m.Color = f.Str()
		case 5:
			m.Time = uint32(f.V)
		}
	})
	return m
}

func decodePayload(b []byte) Payload {
	p := Payload{Items: []Item{}}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			p.HasValue = true
			p.Value = f.Bool()
		case 2:
			it := Item{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					it.ItemID = g.Int32()
				case 2:
					it.Quantity = g.Int32()
				case 3:
					it.IsBlueprint = g.Bool()
				}
			})
			p.Items = append(p.Items, it)
		case 3:
			p.Capacity = f.Int32()
		case 4:
			p.HasProtection = f.Bool()
		case 5:
			p.ProtectionExpiry = uint32(f.V)
		}
	})
	return p
}

func decodeMarker(b []byte) Marker {
	m := Marker{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			m.ID = uint32(f.V)
		case 2:
			m.Type = f.Int32()
		case 3:
			m.X = f.Float()
		case 4:
			m.Y = f.Float()
		case 5:
			m.SteamID = f.V
		case 6:
			m.Rotation = f.Float()
		case 7:
			m.Radius = f.Float()
		case 8:
			i := 0
			pb.Each(f.B, func(g pb.Field) {
				if g.Num >= 1 && g.Num <= 4 {
					m.Color1[g.Num-1] = g.Float()
				}
				i++
			})
		case 10:
			m.Alpha = f.Float()
		case 11:
			m.Name = f.Str()
		case 12:
			m.OutOfStock = f.Bool()
		case 13:
			so := SellOrder{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					so.ItemID = g.Int32()
				case 2:
					so.Quantity = g.Int32()
				case 3:
					so.CurrencyID = g.Int32()
				case 4:
					so.CostPerItem = g.Int32()
				case 5:
					so.AmountInStock = g.Int32()
				case 6:
					so.ItemIsBlueprint = g.Bool()
				case 7:
					so.CurrencyIsBlueprint = g.Bool()
				case 8:
					so.ItemCondition = g.Float()
				case 9:
					so.ItemConditionMax = g.Float()
				}
			})
			m.SellOrders = append(m.SellOrders, so)
		}
	})
	return m
}

func decodeResponse(b []byte) *Response {
	r := &Response{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			r.Seq = uint32(f.V)
		case 4:
			r.Success = true
		case 5:
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					r.Error = g.Str()
				}
			})
			if r.Error == "" {
				r.Error = "unknown error"
			}
		case 6:
			r.Info = decodeInfo(f.B)
		case 7:
			r.Time = decodeTime(f.B)
		case 8:
			r.Map = decodeMap(f.B)
		case 9:
			r.TeamInfo = decodeTeamInfo(f.B)
		case 10:
			r.HasChat = true
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					r.TeamChat = append(r.TeamChat, decodeTeamMessage(g.B))
				}
			})
		case 11:
			e := &EntityInfo{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					e.Type = g.Int32()
				case 3:
					e.Payload = decodePayload(g.B)
				}
			})
			r.EntityInfo = e
		case 12:
			v := false
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					v = g.Bool()
				}
			})
			r.Flag = &v
		case 13:
			r.HasMarkers = true
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					r.Markers = append(r.Markers, decodeMarker(g.B))
				}
			})
		}
	})
	return r
}

func decodeBroadcast(b []byte) *Broadcast {
	bc := &Broadcast{}
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 4:
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 2 {
					bc.TeamChanged = decodeTeamInfo(g.B)
				}
			})
		case 5:
			pb.Each(f.B, func(g pb.Field) {
				if g.Num == 1 {
					m := decodeTeamMessage(g.B)
					bc.TeamMessage = &m
				}
			})
		case 6:
			ec := &EntityChanged{}
			pb.Each(f.B, func(g pb.Field) {
				switch g.Num {
				case 1:
					ec.EntityID = uint32(g.V)
				case 2:
					ec.Payload = decodePayload(g.B)
				}
			})
			bc.EntityChanged = ec
		}
	})
	return bc
}

// DecodeAppMessage splits an AppMessage into response / broadcast.
func DecodeAppMessage(b []byte) (*Response, *Broadcast) {
	var resp *Response
	var bc *Broadcast
	pb.Each(b, func(f pb.Field) {
		switch f.Num {
		case 1:
			resp = decodeResponse(f.B)
		case 2:
			bc = decodeBroadcast(f.B)
		}
	})
	return resp, bc
}
