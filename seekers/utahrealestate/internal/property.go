package internal

import github.com/guregu/null/v5

type Property struct {
	ListNo             int         `json:"listno"`
	Dom                null.String      `json:"dom"`
	Image              string      `json:"image"`
	TimeClause         string      `json:"timeclause"`
	ShortSale          string      `json:"shortsale"`
	Address            string      `json:"address"`
	DimAcres           string      `json:"dim_acres"`
	ConstStatus        int         `json:"conststatus"`
	ListPrice          int         `json:"listprice"`
	ListPriceOriginal  int         `json:"listprice_original"`
	ListPricePrevious  interface{} `json:"listprice_previous"`
	TotBed             int         `json:"tot_bed"`
	TotBath            string      `json:"tot_bath"`
	TotSqf             int         `json:"tot_sqf"`
	Office             int         `json:"office"`
	OpenHouseCount     int         `json:"openhouse_count"`
	Latitude           string      `json:"latitude"`
	Longitude          string      `json:"longitude"`
	DtList             string      `json:"dt_list"`
	Status             int         `json:"status"`
	DtSold             interface{} `json:"dt_sold"`
	DtCanceled         interface{} `json:"dt_canceled"`
	DtExpire           string      `json:"dt_expire"`
	YearBlt            int         `json:"yearblt"`
	RatingID           interface{} `json:"rating_id"`
	ClientID           interface{} `json:"client_id"`
	Rating             interface{} `json:"rating"`
	AgentID            interface{} `json:"agent_id"`
	Visibility         interface{} `json:"visibility"`
	BrandedURLID       interface{} `json:"branded_url_id"`
	CityName           string      `json:"city_name"`
	State              string      `json:"state"`
	CountyName         string      `json:"county_name"`
	Zip                string      `json:"zip"`
	Inetvis            int         `json:"inetvis"`
	NonStandAddress    int         `json:"nonstandaddress"`
	PhotoCount         int         `json:"photocount"`
	PropType           int         `json:"proptype"`
	OffName            string      `json:"offname"`
	PhName             string      `json:"PhName"`
	NextOpenhouseStart string      `json:"next_openhouse_start"`
	NextOpenhouse      string      `json:"next_openhouse"`
	OpID               int         `json:"op_id"`
	OpenhouseURL       interface{} `json:"openhouse_url"`
	OpenhouseTS        string      `json:"openhouse_ts"`
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
	Photos             []Photo     `json:"photos"`
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
