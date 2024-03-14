package lib

import "github.com/guregu/null/v5"

type Property struct {
	ListNo             int         `json:"listno"`
	Dom                null.Int    `json:"dom"`
	Image              string      `json:"image"`
	TimeClause         null.Int    `json:"timeclause"`
	ShortSale          null.Int    `json:"shortsale"`
	Address            string      `json:"address"`
	DimAcres           string      `json:"dim_acres"`
	ConstStatus        int         `json:"conststatus"`
	ListPrice          int         `json:"listprice"`
	ListPriceOriginal  int         `json:"listprice_original"`
	ListPricePrevious  null.Int    `json:"listprice_previous"`
	TotBed             int         `json:"tot_bed"`
	TotBath            string      `json:"tot_bath"`
	TotSqf             int         `json:"tot_sqf"`
	Office             int         `json:"office"`
	OpenHouseCount     int         `json:"openhouse_count"`
	Latitude           string      `json:"latitude"`
	Longitude          string      `json:"longitude"`
	DtList             string      `json:"dt_list"`
	Status             int         `json:"status"`
	DtSold             null.String `json:"dt_sold"`
	DtCanceled         null.String `json:"dt_canceled"`
	DtExpire           string      `json:"dt_expire"`
	YearBlt            int         `json:"yearblt"`
	RatingID           null.Int    `json:"rating_id"`
	ClientID           null.Int    `json:"client_id"`
	Rating             null.Int    `json:"rating"`
	AgentID            null.Int    `json:"agent_id"`
	Visibility         null.Int    `json:"visibility"`
	BrandedURLID       null.Int    `json:"branded_url_id"`
	CityName           string      `json:"city_name"`
	State              string      `json:"state"`
	CountyName         string      `json:"county_name"`
	Zip                string      `json:"zip"`
	Inetvis            int         `json:"inetvis"`
	NonStandAddress    null.Int    `json:"nonstandaddress"`
	PhotoCount         int         `json:"photocount"`
	PropType           int         `json:"proptype"`
	OffName            string      `json:"offname"`
	PhName             string      `json:"PhName"`
	NextOpenhouseStart null.String `json:"next_openhouse_start"`
	NextOpenhouse      null.String `json:"next_openhouse"`
	OpID               null.Int    `json:"op_id"`
	OpenhouseURL       null.String `json:"openhouse_url"`
	OpenhouseTS        null.String `json:"openhouse_ts"`
	ConstStatusTx      string      `json:"conststatus_tx"`
	OpenhouseCountTx   string      `json:"openhouse_count_tx"`
	PropTypeTx         string      `json:"proptype_tx"`
	StatusTx           string      `json:"status_tx"`
	StateTx            string      `json:"state_tx"`
	CtDom              string      `json:"ct_dom"`
	Historical         bool        `json:"historical"`
	DirPre             string      `json:"dir_pre"`
	DirPost            string      `json:"dir_post"`
	DirNs              string      `json:"dir_ns"`
	DirEw              string      `json:"dir_ew"`
	Photos             []*Photo    `json:"photos"`
}

type Photo struct {
	ListNo     int    `json:"ListNo"`
	PhName     string `json:"PhName"`
	PhRoomType string `json:"PhRoomType"`
	PhLocation string `json:"PhLocation"`
	PhOrder    int    `json:"PhOrder"`
	PhDesc     string `json:"PhDesc"`
	PhTitle    string `json:"PhTitle"`
	Desc       string `json:"desc"`
	PhCaption  string `json:"PhCaption"`
	PhTags1    int    `json:"PhTags1"`
	PhTags2    int    `json:"PhTags2"`
	PhTags3    int    `json:"PhTags3"`
	Thumb      string `json:"thumb"`
	Small      string `json:"small"`
	Medium     string `json:"medium"`
	Large      string `json:"large"`
	XLarge     string `json:"x-large"`
	Original   string `json:"original"`
}
