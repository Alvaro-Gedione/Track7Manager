package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================
// CONSTANTES DO PROTOCOLO
// ============================================

// ============================================
// ESTRUTURAS DE DADOS
// ============================================

type TelemetriaRecord struct {
	IMEI        string                 `json:"imei"`
	Timestamp   time.Time              `json:"timestamp"`
	Latitude    float64                `json:"latitude,omitempty"`
	Longitude   float64                `json:"longitude,omitempty"`
	Velocidade  float32                `json:"velocidade,omitempty"`
	RPM         int                    `json:"rpm,omitempty"`
	CoordsValid bool                   `json:"coords_valid"`
	Atributos   map[string]interface{} `json:"atributos"`
}

type EventoRecord struct {
	IMEI      string    `json:"imei"`
	Tipo      int       `json:"tipo"`
	Duracao   int       `json:"duracao"`
	Pico      int       `json:"pico"`
	Timestamp time.Time `json:"timestamp"`
}

type ViagemRecord struct {
	IMEI      string    `json:"imei"`
	Inicio    time.Time `json:"inicio"`
	Motorista int       `json:"motorista"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Distancia int       `json:"distancia"`
}

// ============================================
// SUPABASE CLIENT
// ============================================

type SupabaseClient struct {
	pool   *pgxpool.Pool
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	stats  Stats
}

type Stats struct {
	TotalRecords    int64
	TotalBatches    int64
	FailedBatches   int64
	SkippedNoCoords int64
	LastBatchTime   time.Time
	LastBatchSize   int
	LastError       string
	mu              sync.RWMutex
}

var supabase *SupabaseClient

func NewSupabaseClient(connString string) (*SupabaseClient, error) {
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("erro ao parsear connection string: %v", err)
	}

	config.MaxConns = 10
	config.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("erro ao conectar: %v", err)
	}

	fmt.Println("✅ Conectado ao Supabase com sucesso!")

	ctx, cancel := context.WithCancel(context.Background())

	client := &SupabaseClient{
		pool:   pool,
		ctx:    ctx,
		cancel: cancel,
	}

	return client, nil
}

func (s *SupabaseClient) AddTelemetria(record TelemetriaRecord) {
	// Só insere se tiver coordenadas válidas OU velocidade > 0
	if !record.CoordsValid && record.Velocidade == 0 {
		s.stats.mu.Lock()
		s.stats.SkippedNoCoords++
		s.stats.mu.Unlock()
		return
	}

	query := `INSERT INTO telemetria_live (imei, timestamp, latitude, longitude, velocidade, rpm, atributos)
			  VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`

	atributosJSON, _ := json.Marshal(record.Atributos)

	_, err := s.pool.Exec(context.Background(), query,
		record.IMEI, record.Timestamp, record.Latitude, record.Longitude,
		record.Velocidade, record.RPM, string(atributosJSON))

	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	s.stats.LastBatchTime = time.Now()

	if err != nil {
		s.stats.FailedBatches++
		s.stats.LastError = err.Error()
		fmt.Printf("❌ Erro ao inserir telemetria: %v\n", err)
	} else {
		s.stats.TotalRecords++
		s.stats.TotalBatches++
		
		if record.CoordsValid {
			fmt.Printf("📍 [%s] GPS: %.6f,%.6f | Vel:%.0f km/h | IMEI:%s\n",
				record.Timestamp.Format("15:04:05"), record.Latitude, record.Longitude, record.Velocidade, record.IMEI)
		} else if record.Velocidade > 0 {
			fmt.Printf("🚗 [%s] CAN: Vel=%.0f km/h, RPM=%d | IMEI:%s\n",
				record.Timestamp.Format("15:04:05"), record.Velocidade, record.RPM, record.IMEI)
		}
	}
}

func (s *SupabaseClient) AddEvento(evento EventoRecord) {
	query := `INSERT INTO eventos (imei, tipo, duracao, pico, timestamp)
			  VALUES ($1, $2, $3, $4, $5)`

	_, err := s.pool.Exec(context.Background(), query,
		evento.IMEI, evento.Tipo, evento.Duracao, evento.Pico, evento.Timestamp)

	if err != nil {
		fmt.Printf("❌ Erro ao inserir evento: %v\n", err)
	} else {
		fmt.Printf("⚠️ EVENTO: IMEI=%s, Tipo=%d (%s), Duração=%ds\n",
			evento.IMEI, evento.Tipo, mapEvento(uint8(evento.Tipo)), evento.Duracao)
	}
}

func (s *SupabaseClient) AddViagem(viagem ViagemRecord) {
	query := `INSERT INTO viagens (imei, inicio, motorista, latitude, longitude, distancia)
			  VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := s.pool.Exec(context.Background(), query,
		viagem.IMEI, viagem.Inicio, viagem.Motorista, viagem.Latitude, viagem.Longitude, viagem.Distancia)

	if err != nil {
		fmt.Printf("❌ Erro ao inserir viagem: %v\n", err)
	} else {
		fmt.Printf("🚀 VIAGEM INICIADA: IMEI=%s, Motorista=%d, Local: (%.6f, %.6f)\n", 
			viagem.IMEI, viagem.Motorista, viagem.Latitude, viagem.Longitude)
	}
}

func (s *SupabaseClient) GetStats() Stats {
	s.stats.mu.RLock()
	defer s.stats.mu.RUnlock()
	return s.stats
}

func (s *SupabaseClient) Close() {
	fmt.Println("🛑 Desligando conexão com Supabase...")
	s.cancel()
	s.wg.Wait()
	s.pool.Close()
	fmt.Println("✅ Conexão com Supabase encerrada")
}

// ============================================
// BIT READER (CORRIGIDO - LSB FIRST)
// ============================================

type BitReader struct {
	data []byte
	pos  uint
}

func (r *BitReader) ReadBits(bits uint) uint64 {
	var res uint64
	for i := uint(0); i < bits; i++ {
		bytePos := r.pos / 8
		bitPos := r.pos % 8
		if bytePos >= uint(len(r.data)) {
			break
		}
		if (r.data[bytePos] & (1 << bitPos)) != 0 {
			res |= (1 << i)
		}
		r.pos++
	}
	return res
}

func (r *BitReader) RemainingBits() uint {
	return uint(len(r.data))*8 - r.pos
}

// ============================================
// FUNÇÕES AUXILIARES
// ============================================

func mapEvento(id uint8) string {
	events := map[uint8]string{
		101: "Acel. Brusca",
		102: "Freada Brusca",
		103: "Excesso RPM",
		105: "Marcha Lenta",
		106: "Transmissao",
		107: "Curva Brusca",
		108: "Excesso Velocidade",
		109: "RPM Parado",
		110: "Fora Faixa Verde",
	}
	if val, ok := events[id]; ok {
		return val
	}
	return fmt.Sprintf("Evento %d", id)
}

func readIMEI(reader *BitReader) string {
	high := reader.ReadBits(24)
	low := reader.ReadBits(27)
	return fmt.Sprintf("%d%08d", high, low)
}

// ============================================
// SERVIDOR PRINCIPAL
// ============================================

func main() {
	// Configuração do Supabase
	dbUser := "postgres.uoovvwxnerpqeksuogvi"
	dbPassword := "r8xLrSZp7yY%25%23k%2A"
	dbHost := "aws-1-sa-east-1.pooler.supabase.com"
	dbPort := 6543
	dbName := "postgres"

	connString := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=require&default_query_exec_mode=simple_protocol",
		dbUser, dbPassword, dbHost, dbPort, dbName)

	var err error
	supabase, err = NewSupabaseClient(connString)
	if err != nil {
		fmt.Printf("❌ Erro fatal ao conectar ao Supabase: %v\n", err)
		return
	}
	defer supabase.Close()

	fmt.Println("=== SERVIDOR GALILEOSKY V3.2 COM SUPABASE ===")
	fmt.Println("Escutando na porta 4040...")

	ln, err := net.Listen("tcp", "0.0.0.0:4040")
	if err != nil {
		fmt.Printf("Erro ao iniciar servidor: %v\n", err)
		return
	}
	defer ln.Close()

	// Goroutine para mostrar estatísticas
	go showStats()

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn)
	}
}

func showStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if supabase == nil {
			return
		}
		stats := supabase.GetStats()
		fmt.Printf("\n📊 ESTATÍSTICAS:\n")
		fmt.Printf("   Registros salvos: %d\n", stats.TotalRecords)
		fmt.Printf("   Registros ignorados (sem GPS): %d\n", stats.SkippedNoCoords)
		fmt.Printf("   Batches com erro: %d\n", stats.FailedBatches)
		if stats.LastBatchTime.Unix() > 0 {
			fmt.Printf("   Último insert: %s\n", stats.LastBatchTime.Format("15:04:05"))
		}
		fmt.Println()
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr().String()
	fmt.Printf("[%s] Dispositivo conectado\n", addr)

	var stream []byte // Buffer que junta fatias do TCP
	buffer := make([]byte, 4096)

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Printf("[%s] Dispositivo desconectado (EOF)\n", addr)
			return
		}

		// Adiciona o que chegou ao nosso fluxo contínuo
		stream = append(stream, buffer[:n]...)

		// Tenta extrair todos os pacotes completos disponíveis no stream
		for len(stream) > 0 {
			consumed := tryParsePacket(stream)
			if consumed == 0 {
				// Faltam pedaços do pacote (TCP fatiado). Espera o próximo Read.
				break
			}
			// Avança o stream removendo o pacote que já processamos
			stream = stream[consumed:]
		}
	}
}

func tryParsePacket(data []byte) int {
	if len(data) < 1 {
		return 0
	}

	reader := &BitReader{data: data, pos: 0}
	headerID := uint8(reader.ReadBits(3))

	// Função de segurança: verifica se temos bits suficientes no stream
	hasBits := func(bits uint) bool { return uint(len(data))*8 >= bits }

	switch headerID {
	case 0: // INICIO_VIAGEM
		if !hasBits(150) {
			return 0
		} // Precisa de 19 bytes
		imei := readIMEI(reader)
		ts := reader.ReadBits(32)
		lon := int32(reader.ReadBits(32))
		lat := int32(reader.ReadBits(32))
		
		// Envia para o Supabase
		viagem := ViagemRecord{
			IMEI:      imei,
			Inicio:    time.Unix(int64(ts), 0),
			Latitude:  float64(lat) / 1e6,
			Longitude: float64(lon) / 1e6,
			Distancia: 0,
		}
		supabase.AddViagem(viagem)
		
		printBox("INICIO VIAGEM", imei, ts, fmt.Sprintf("Lat: %.6f | Lon: %.6f", float64(lat)/1e6, float64(lon)/1e6))
		return 19

	case 1: // IDENT_MOTORISTA
		if !hasBits(118) {
			return 0
		} // Precisa de 15 bytes
		imei := readIMEI(reader)
		driver := reader.ReadBits(32)
		ts := reader.ReadBits(32)
		
		// Nota: O motorista é salvo junto com a viagem, não temos uma tabela específica para identificação isolada
		fmt.Printf("[%s] 👤 Motorista ID: %d identificado\n", imei, driver)
		
		printBox("IDENT. MOTORISTA", imei, ts, fmt.Sprintf("Motorista ID: %d", driver))
		return 15

	case 2: // 30_SEGUNDOS
		if !hasBits(159) {
			return 0
		} // Precisa de 20 bytes
		imei := readIMEI(reader)
		valid := reader.ReadBits(1)
		lon := int32(reader.ReadBits(32))
		lat := int32(reader.ReadBits(32))
		speed := reader.ReadBits(8)
		ts := reader.ReadBits(32)
		
		// Envia para o Supabase
		telemetria := TelemetriaRecord{
			IMEI:        imei,
			Timestamp:   time.Unix(int64(ts), 0),
			Velocidade:  float32(speed),
			CoordsValid: valid == 1,
			Atributos: map[string]interface{}{
				"coords_valid": valid == 1,
				"tipo":         "periodico_30s",
			},
		}
		
		if valid == 1 {
			telemetria.Latitude = float64(lat) / 1e6
			telemetria.Longitude = float64(lon) / 1e6
		}
		
		supabase.AddTelemetria(telemetria)
		
		status := "GPS OK"
		if valid == 0 {
			status = "GPS Sem Sinal"
		}
		printBox("POSICAO (30s)", imei, ts, fmt.Sprintf("%s | Vel: %d km/h | Lat: %.6f | Lon: %.6f", status, speed, float64(lat)/1e6, float64(lon)/1e6))
		return 20

	case 5: // MULTI_EVENTOS
		if !hasBits(102) {
			return 0
		} // Garante que podemos ler pelo menos o count
		imei := readIMEI(reader)
		distancia := reader.ReadBits(32)
		count := reader.ReadBits(16)

		// Header(102) + Count * (69 bits de dado + 3 bits de padding = 72)
		totalBits := 102 + uint(count)*72
		if !hasBits(totalBits) {
			return 0
		} // Aguarda resto do pacote chegar

		fmt.Printf("\n>>> [%s] MULTI-EVENTOS Recv: %d | Hodometro: %d m\n", imei, count, distancia)
		
		for i := 0; i < int(count); i++ {
			id := reader.ReadBits(7)
			dur := reader.ReadBits(17)
			peak := reader.ReadBits(13)
			ts := reader.ReadBits(32)
			
			// Envia evento para o Supabase
			evento := EventoRecord{
				IMEI:      imei,
				Tipo:      int(id),
				Duracao:   int(dur),
				Pico:      int(peak),
				Timestamp: time.Unix(int64(ts), 0),
			}
			supabase.AddEvento(evento)
			
			// PULA O LIXO GERADO PELO FILEWRITE DO GALILEO! (3 bits de padding)
			reader.ReadBits(3)
			
			fmt.Printf("  Event #%d | %-18s | Dur: %ds | Pico: %d | %s\n",
				i+1, mapEvento(uint8(id)), dur, peak, time.Unix(int64(ts), 0).Format("15:04:05"))
		}
		return int((totalBits + 7) / 8)

	case 6: // MULTI_DADOS
		if !hasBits(78) {
			return 0
		} // Garante que podemos ler pelo menos o count
		imei := readIMEI(reader)
		sensores := reader.ReadBits(8)
		count := reader.ReadBits(16)

		// Header(78) + Count * (53 bits de dado + 3 bits de padding = 56)
		totalBits := 78 + uint(count)*56
		if !hasBits(totalBits) {
			return 0
		} // Aguarda resto do arquivo chegar da rede

		fmt.Printf("\n>>> [%s] MULTI-DADOS Recv: %d registros | Bits Sensores: %08b\n", imei, count, sensores)
		
		for i := 0; i < int(count); i++ {
			rpm := reader.ReadBits(13)
			speed := reader.ReadBits(8)
			ts := reader.ReadBits(32)
			
			// Envia dados CAN para o Supabase
			telemetria := TelemetriaRecord{
				IMEI:        imei,
				Timestamp:   time.Unix(int64(ts), 0),
				Velocidade:  float32(speed),
				RPM:         int(rpm),
				CoordsValid: false,
				Atributos: map[string]interface{}{
					"fonte":      "CAN_batch",
					"tipo":       "dados_motor",
					"sensores": map[string]bool{
						"freio":      (sensores & 0x01) != 0,
						"acelerador": (sensores & 0x02) != 0,
						"embreagem":  (sensores & 0x04) != 0,
					},
				},
			}
			supabase.AddTelemetria(telemetria)
			
			// PULA O LIXO GERADO PELO FILEWRITE DO GALILEO! (3 bits de padding)
			reader.ReadBits(3)

			if i < 3 || i >= int(count)-3 {
				fmt.Printf("  Data #%d | RPM: %4d | Speed: %3d km/h | Time: %s\n",
					i+1, rpm, speed, time.Unix(int64(ts), 0).Format("15:04:05"))
			} else if i == 3 {
				fmt.Println("  ...")
			}
		}
		return int((totalBits + 7) / 8)

	default:
		// Se o byte não bater com nada, avança 1 byte para o leitor se resincronizar
		fmt.Printf("[DEBUG] Header %d ignorado. Resincronizando... Byte: %02X\n", headerID, data[0])
		return 1
	}
}

func printBox(title, imei string, ts uint64, extra string) {
	tStr := "Data Invalida"
	if ts > 0 {
		tStr = time.Unix(int64(ts), 0).Format("02/01/2006 15:04:05")
	}
	fmt.Println("\n+------------------------------------------------------------+")
	fmt.Printf("| %-58s |\n", title)
	fmt.Printf("| IMEI: %-52s |\n", imei)
	fmt.Printf("| Hora: %-52s |\n", tStr)
	fmt.Printf("| %-58s |\n", extra)
	fmt.Println("+------------------------------------------------------------+")
}