package models

import (
	"errors"
	"sort"
	"strings"
)

// The country registry.
//
// ---------------------------------------------------------------------------
// WHY A CLOSED SET IN GO AND A SHAPE CHECK IN SQL
// ---------------------------------------------------------------------------
//
// This repository already has a house form for "a value from a set nobody may
// invent": a map in models, consulted before anything is stored, beside a
// database constraint that refuses what Go would not have produced. Scopes,
// API environments, event decisions and command specs are all that shape, and
// country is written the same way rather than as a new mechanism.
//
// WHERE IT DIFFERS FROM Scopes, AND WHY. The scope CHECK in migrations/030
// lists all seven scopes, because that set changes only when this codebase
// changes and a migration alongside the code is honest. The alpha-2 set is 249
// entries maintained by ISO; a CHECK holding them would have to be migrated
// whenever a code is assigned or withdrawn, and until that migration ran the
// database would refuse a country that legitimately exists. So SQL enforces the
// SHAPE (two uppercase letters, migrations/037) and this file enforces
// MEMBERSHIP. Storage can never hold something malformed; the API can never
// accept something unassigned.
//
// ---------------------------------------------------------------------------
// ALPHA-2, NOT A NAME AND NOT ALPHA-3
// ---------------------------------------------------------------------------
//
// A country NAME is not an identifier -- it is spelled differently in every
// system that holds one, and "Côte d'Ivoire" in one place and "Ivory Coast" in
// another is a reconciliation problem a customer discovers late. Alpha-2 is
// what every other system the customer already runs uses, and it is what the
// site's timezone sits beside naturally.
//
// USER-ASSIGNED AND EXCEPTIONALLY RESERVED CODES ARE NOT IN THIS SET. AA, QM-QZ,
// XA-XZ and ZZ are reserved for private use and are not countries; accepting one
// would put a value in a customer's record that no other system can interpret.

// ErrUnknownCountry is a code that is not an assigned ISO 3166-1 alpha-2
// element. Refused rather than stored: a country nobody can resolve is worse
// than no country, because a reader believes it.
var ErrUnknownCountry = errors.New("country must be an ISO 3166-1 alpha-2 code")

// countries is the assigned ISO 3166-1 alpha-2 set.
//
// A set rather than a map to names: this package validates, it does not
// localise. What a country is CALLED belongs wherever it is displayed, in the
// language of whoever is reading, and a hard-coded English name here would be
// the version everything downstream inherited.
var countries = map[string]bool{
	"AD": true, "AE": true, "AF": true, "AG": true, "AI": true, "AL": true,
	"AM": true, "AO": true, "AQ": true, "AR": true, "AS": true, "AT": true,
	"AU": true, "AW": true, "AX": true, "AZ": true, "BA": true, "BB": true,
	"BD": true, "BE": true, "BF": true, "BG": true, "BH": true, "BI": true,
	"BJ": true, "BL": true, "BM": true, "BN": true, "BO": true, "BQ": true,
	"BR": true, "BS": true, "BT": true, "BV": true, "BW": true, "BY": true,
	"BZ": true, "CA": true, "CC": true, "CD": true, "CF": true, "CG": true,
	"CH": true, "CI": true, "CK": true, "CL": true, "CM": true, "CN": true,
	"CO": true, "CR": true, "CU": true, "CV": true, "CW": true, "CX": true,
	"CY": true, "CZ": true, "DE": true, "DJ": true, "DK": true, "DM": true,
	"DO": true, "DZ": true, "EC": true, "EE": true, "EG": true, "EH": true,
	"ER": true, "ES": true, "ET": true, "FI": true, "FJ": true, "FK": true,
	"FM": true, "FO": true, "FR": true, "GA": true, "GB": true, "GD": true,
	"GE": true, "GF": true, "GG": true, "GH": true, "GI": true, "GL": true,
	"GM": true, "GN": true, "GP": true, "GQ": true, "GR": true, "GS": true,
	"GT": true, "GU": true, "GW": true, "GY": true, "HK": true, "HM": true,
	"HN": true, "HR": true, "HT": true, "HU": true, "ID": true, "IE": true,
	"IL": true, "IM": true, "IN": true, "IO": true, "IQ": true, "IR": true,
	"IS": true, "IT": true, "JE": true, "JM": true, "JO": true, "JP": true,
	"KE": true, "KG": true, "KH": true, "KI": true, "KM": true, "KN": true,
	"KP": true, "KR": true, "KW": true, "KY": true, "KZ": true, "LA": true,
	"LB": true, "LC": true, "LI": true, "LK": true, "LR": true, "LS": true,
	"LT": true, "LU": true, "LV": true, "LY": true, "MA": true, "MC": true,
	"MD": true, "ME": true, "MF": true, "MG": true, "MH": true, "MK": true,
	"ML": true, "MM": true, "MN": true, "MO": true, "MP": true, "MQ": true,
	"MR": true, "MS": true, "MT": true, "MU": true, "MV": true, "MW": true,
	"MX": true, "MY": true, "MZ": true, "NA": true, "NC": true, "NE": true,
	"NF": true, "NG": true, "NI": true, "NL": true, "NO": true, "NP": true,
	"NR": true, "NU": true, "NZ": true, "OM": true, "PA": true, "PE": true,
	"PF": true, "PG": true, "PH": true, "PK": true, "PL": true, "PM": true,
	"PN": true, "PR": true, "PS": true, "PT": true, "PW": true, "PY": true,
	"QA": true, "RE": true, "RO": true, "RS": true, "RU": true, "RW": true,
	"SA": true, "SB": true, "SC": true, "SD": true, "SE": true, "SG": true,
	"SH": true, "SI": true, "SJ": true, "SK": true, "SL": true, "SM": true,
	"SN": true, "SO": true, "SR": true, "SS": true, "ST": true, "SV": true,
	"SX": true, "SY": true, "SZ": true, "TC": true, "TD": true, "TF": true,
	"TG": true, "TH": true, "TJ": true, "TK": true, "TL": true, "TM": true,
	"TN": true, "TO": true, "TR": true, "TT": true, "TV": true, "TW": true,
	"TZ": true, "UA": true, "UG": true, "UM": true, "US": true, "UY": true,
	"UZ": true, "VA": true, "VC": true, "VE": true, "VG": true, "VI": true,
	"VN": true, "VU": true, "WF": true, "WS": true, "YE": true, "YT": true,
	"ZA": true, "ZM": true, "ZW": true,
}

// KnownCountry reports whether a code is an assigned alpha-2 element.
//
// Case-insensitive and whitespace-tolerant, because "ng" from one integration
// and " NG" from another are the same country and refusing either would be a
// contract that punishes formatting rather than meaning.
func KnownCountry(code string) bool {
	return countries[strings.ToUpper(strings.TrimSpace(code))]
}

// NormaliseCountry returns the stored form of a country, or ErrUnknownCountry.
//
// The stored form is upper case, which is what the column CHECK requires -- so
// nothing can reach the database in a case the constraint would refuse, and two
// integrations spelling the same country differently cannot produce two
// different stored values.
func NormaliseCountry(code string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(code))
	if !countries[upper] {
		return "", ErrUnknownCountry
	}
	return upper, nil
}

// AllCountries returns every assigned code, sorted.
//
// Sorted rather than map order so an error message, a test fixture and a
// generated document all read the same way twice in a row -- the same property
// AllScopes exists for.
func AllCountries() []string {
	out := make([]string, 0, len(countries))
	for code := range countries {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
