package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
)

type UserInput struct {
	Height   string `json:"height"`
	Location string `json:"location"`
	Occasion string `json:"occasion"`
	Rating   int    `json:"rating"`
}

type streamChunk struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

type GeoResponse []struct {
	Lat string `json:"lat"`
	Lon string `json:"lon"`
}

type OpenMeteoResponse struct {
	CurrentWeather struct {
		Temperature float64 `json:"temperature"`
		Windspeed   float64 `json:"windspeed"`
	} `json:"current_weather"`
}

var lastPrompt string

func main() {
	log.Println("main: starting server on http://localhost:8080")
	http.HandleFunc("/suggest", suggestHandler)

	fs := http.FileServer(http.Dir("frontend"))
	http.Handle("/", fs)

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("main: server failed:", err)
	}
}

// corsMiddleware adds CORS headers and logs request method and path
// func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
// 	return func(w http.ResponseWriter, r *http.Request) {
// 		log.Printf("corsMiddleware: %s %s", r.Method, r.URL.Path)
// 		w.Header().Set("Access-Control-Allow-Origin", "*")
// 		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
// 		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
// 		if r.Method == http.MethodOptions {
// 			w.WriteHeader(http.StatusNoContent)
// 			return
// 		}
// 		next(w, r)
// 	}
// }

// suggestHandler decodes input, gets lat/lon, fetches weather, calls Ollama and returns suggestion
func suggestHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("suggestHandler: called")
	var input UserInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		log.Println("suggestHandler: JSON decode error", err)
		return
	}
	log.Printf("suggestHandler: input=%+v", input)

	if input.Height == "" || input.Location == "" || input.Occasion == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	lat, lon, err := getLatLon(input.Location)
	if err != nil {
		http.Error(w, "Failed to geocode location", http.StatusInternalServerError)
		log.Println("suggestHandler: geocode error", err)
		return
	}
	log.Printf("suggestHandler: geocoded %s to lat=%.6f lon=%.6f", input.Location, lat, lon)

	temp, condition, err := fetchWeather(lat, lon)
	if err != nil {
		http.Error(w, "Failed to fetch weather data", http.StatusInternalServerError)
		log.Println("suggestHandler: weather fetch error", err)
		return
	}
	log.Printf("suggestHandler: weather temp=%.1f condition=%s", temp, condition)

	prompt := fmt.Sprintf(
		"Suggest clothing for a person with height %s attending a %s in %s weather with temperature %.1f°C.",
		input.Height, input.Occasion, condition, temp,
	)
	if input.Rating > 0 && input.Rating < 8 {
		prompt += " The previous suggestion wasn't good enough. Make a better one."
	}
	lastPrompt = prompt
	log.Println("suggestHandler: prompt=", prompt)

	suggestion, err := callOllama(prompt)
	if err != nil {
		http.Error(w, "Failed to get AI suggestion", http.StatusInternalServerError)
		log.Println("suggestHandler: Ollama error", err)
		return
	}
	log.Println("suggestHandler: suggestion=", suggestion)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"suggestion": suggestion})
}

// getLatLon calls Nominatim to convert city name to latitude and longitude
func getLatLon(city string) (float64, float64, error) {
	url := fmt.Sprintf("https://nominatim.openstreetmap.org/search?q=%s&format=json&limit=1", city)
	log.Println("getLatLon: url=", url)
	resp, err := http.Get(url)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	var geo GeoResponse
	if err := json.NewDecoder(resp.Body).Decode(&geo); err != nil {
		return 0, 0, err
	}
	if len(geo) == 0 {
		return 0, 0, fmt.Errorf("no results for city %s", city)
	}
	lat, _ := strconv.ParseFloat(geo[0].Lat, 64)
	lon, _ := strconv.ParseFloat(geo[0].Lon, 64)
	log.Printf("getLatLon: result lat=%.6f lon=%.6f", lat, lon)
	return lat, lon, nil
}

// fetchWeather calls Open-Meteo to get current temperature and a basic condition
func fetchWeather(lat, lon float64) (float64, string, error) {
	url := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.6f&longitude=%.6f&current_weather=true", lat, lon)
	log.Println("fetchWeather: url=", url)
	resp, err := http.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	var om OpenMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&om); err != nil {
		return 0, "", err
	}
	temp := om.CurrentWeather.Temperature
	condition := "clear"
	log.Printf("fetchWeather: returned temp=%.1f winds=%.1f", temp, om.CurrentWeather.Windspeed)
	return temp, condition, nil
}

// callOllama streams responses from Ollama and concatenates them
func callOllama(prompt string) (string, error) {
	log.Println("callOllama: sending prompt to Ollama")
	reqBody := map[string]string{"prompt": prompt, "model": "llama3.2"}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := http.Post("http://localhost:11434/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var fullResponse string
	for scanner.Scan() {
		line := scanner.Bytes()
		var chunk streamChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		fullResponse += chunk.Response
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fullResponse, err
	}
	log.Println("callOllama: fullResponse=", fullResponse)
	return fullResponse, nil
}
