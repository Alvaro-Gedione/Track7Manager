package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================
// ESTRUTURAS DO PROTOCOLO GALILEOSKY
// ============================================

type GalileoPacket struct {
	Header        byte
	Length        uint16
	HasUnsentData bool
	RawData       []byte
	Checksum      uint16
	IsValid       bool
	Positions     []Position
}

type Position struct {
	DeviceID   string
	Time       time.Time
	Valid      bool
	Latitude   float64
	Longitude  float64
	Altitude   float64
	Speed      float64
	Course     float64
	Attributes map[string]interface{}
	Alarm      string
}

// ============================================
// CONFIGURAÇÕES DO SUPABASE
// ============================================

type SupabaseConfig struct {
	Host        string // ex: aws-0-sa-east-1.pooler.supabase.com
	Port        int    // 6543 (Transaction pooler)
	User        string // postgres.[project_id]
	Password    string // sua senha do banco
	Database    string // postgres
	ServiceRole string // chave service_role para escrita
}

var supabaseConfig = SupabaseConfig{
	Host:        "aws-1-sa-east-1.pooler.supabase.com", // Substitua pela sua URL
	Port:        6543,
	User:        "postgres.uoovvwxnerpqeksuogvi", // Substitua
	Password:    "5fz6QC36ndMlsjYKs",    // Substitua
	Database:    "postgres",
	ServiceRole: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InVvb3Z2d3huZXJwcWVrc3VvZ3ZpIiwicm9sZSI6InNlcnZpY2Vfcm9sZSIsImlhdCI6MTc3NDQ2NDM3MCwiZXhwIjoyMDkwMDQwMzcwfQ.VLavKtGeBCx-Wdbt53uNxz4nXNk992A93etso7v-gOs", // Substitua pela sua service_role key
}

// ============================================
// CONFIGURAÇÕES DE BATCHING
// ============================================

type BatchConfig struct {
	MaxSize     int           // Máximo de registros por batch (500-1000)
	MaxInterval time.Duration // Tempo máximo para acumular batch (2 segundos)
}

var batchConfig = BatchConfig{
	MaxSize:     500,              // 500 registros por batch
	MaxInterval: 2 * time.Second, // Envia a cada 2 segundos mesmo sem atingir MaxSize
}

// ============================================
// ESTRUTURAS DE DADOS DO SUPABASE
// ============================================

type TelemetriaRecord struct {
	IMEI       string                 `json:"imei"`
	Timestamp  time.Time              `json:"timestamp"`
	Latitude   float64                `json:"latitude,omitempty"`
	Longitude  float64                `json:"longitude,omitempty"`
	Velocidade float32                `json:"velocidade,omitempty"`
	Atributos  map[string]interface{} `json:"atributos"`
}

// ============================================
// BATCH PROCESSOR
// ============================================

type BatchProcessor struct {
	pool   *pgxpool.Pool
	queue  chan TelemetriaRecord
	batch  []TelemetriaRecord
	mutex  sync.Mutex
	ticker *time.Ticker
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	stats  BatchStats
}

type BatchStats struct {
	TotalRecords  int64
	TotalBatches  int64
	FailedBatches int64
	LastBatchTime time.Time
	LastBatchSize int
	LastError     string
	mu            sync.RWMutex
}

func NewBatchProcessor(connString string) (*BatchProcessor, error) {
	ctx := context.Background()

	// Configura pool de conexões otimizado para alto volume
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("erro ao parsear connection string: %v", err)
	}

	// Otimizações para alto throughput
	config.MaxConns = 20
	config.MinConns = 5
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar pool de conexões: %v", err)
	}

	// Testa conexão
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("erro ao conectar ao Supabase: %v", err)
	}

	fmt.Println("✅ Conectado ao Supabase com sucesso!")

	ctx, cancel := context.WithCancel(context.Background())

	bp := &BatchProcessor{
		pool:   pool,
		queue:  make(chan TelemetriaRecord, 10000),
		batch:  make([]TelemetriaRecord, 0, batchConfig.MaxSize),
		ticker: time.NewTicker(batchConfig.MaxInterval),
		ctx:    ctx,
		cancel: cancel,
		stats:  BatchStats{}, // Inicializa stats vazio
	}

	// Inicia worker de processamento
	bp.wg.Add(1)
	go bp.processBatchWorker()

	return bp, nil
}

func (bp *BatchProcessor) AddRecord(record TelemetriaRecord) {
	select {
	case bp.queue <- record:
		// Registro adicionado à fila
		bp.stats.mu.Lock()
		bp.stats.TotalRecords++
		bp.stats.mu.Unlock()
	default:
		// Fila cheia - log de aviso
		fmt.Printf("⚠️  Fila cheia! Registro perdido para IMEI: %s\n", record.IMEI)
	}
}

func (bp *BatchProcessor) processBatchWorker() {
	defer bp.wg.Done()

	var pendingBatch []TelemetriaRecord

	for {
		select {
		case <-bp.ctx.Done():
			// Processa último batch antes de fechar
			if len(pendingBatch) > 0 {
				bp.flushBatch(pendingBatch)
			}
			return

		case record := <-bp.queue:
			pendingBatch = append(pendingBatch, record)

			if len(pendingBatch) >= batchConfig.MaxSize {
				bp.flushBatch(pendingBatch)
				pendingBatch = make([]TelemetriaRecord, 0, batchConfig.MaxSize)
			}

		case <-bp.ticker.C:
			if len(pendingBatch) > 0 {
				bp.flushBatch(pendingBatch)
				pendingBatch = make([]TelemetriaRecord, 0, batchConfig.MaxSize)
			}
		}
	}
}

func (bp *BatchProcessor) flushBatch(batch []TelemetriaRecord) {
	if len(batch) == 0 {
		return
	}
	startTime := time.Now()

	// Adicionamos o ::jsonb explicitamente na query para evitar o erro 22P02
	query := `
		INSERT INTO telemetria_live (imei, timestamp, latitude, longitude, velocidade, atributos)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`

	batchCmd := &pgx.Batch{}

	for _, record := range batch {
		atributosJSON, err := json.Marshal(record.Atributos)
		if err != nil {
			continue
		}
		
		batchCmd.Queue(query,
			record.IMEI,
			record.Timestamp,
			record.Latitude,
			record.Longitude,
			record.Velocidade,
			string(atributosJSON), // Enviamos como string e o banco converte pelo ::jsonb
		)
	}

	if batchCmd.Len() == 0 { return }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	br := bp.pool.SendBatch(ctx, batchCmd)
	
	var lastErr error
	successCount := 0
	for i := 0; i < batchCmd.Len(); i++ {
		_, execErr := br.Exec()
		if execErr != nil {
			lastErr = execErr
		} else {
			successCount++
		}
	}
	br.Close()

	// Atualiza stats
	bp.stats.mu.Lock()
	bp.stats.TotalBatches++
	bp.stats.LastBatchTime = startTime
	bp.stats.LastBatchSize = successCount
	bp.stats.TotalRecords += int64(successCount)
	if lastErr != nil {
		bp.stats.FailedBatches++
		bp.stats.LastError = lastErr.Error()
	}
	bp.stats.mu.Unlock()

	if successCount > 0 {
		fmt.Printf("✅ %d registros inseridos no Supabase em %.2fms\n", successCount, float64(time.Since(startTime).Microseconds())/1000.0)
	}
}

func (bp *BatchProcessor) Close() {
	fmt.Println("🛑 Desligando BatchProcessor...")
	bp.cancel()
	bp.ticker.Stop()
	bp.wg.Wait()
	bp.pool.Close()
	fmt.Println("✅ BatchProcessor desligado")
}

func (bp *BatchProcessor) GetStats() BatchStats {
	bp.stats.mu.RLock()
	defer bp.stats.mu.RUnlock()
	return bp.stats
}

// ============================================
// MAPEAMENTO DE TAGS DO GALILEOSKY
// ============================================

var tagLengthMap = map[byte]int{
	// Tags de 1 byte
	0x01: 1, 0x02: 1, 0x35: 1, 0x43: 1, 0xc4: 1, 0xc5: 1, 0xc6: 1, 0xc7: 1,
	0xc8: 1, 0xc9: 1, 0xca: 1, 0xcb: 1, 0xcc: 1, 0xcd: 1, 0xce: 1, 0xcf: 1,
	0xd0: 1, 0xd1: 1, 0xd2: 1, 0xd5: 1, 0x88: 1, 0x8a: 1, 0x8b: 1, 0x8c: 1,
	0xa0: 1, 0xaf: 1, 0xa1: 1, 0xa2: 1, 0xa3: 1, 0xa4: 1, 0xa5: 1, 0xa6: 1,
	0xa7: 1, 0xa8: 1, 0xa9: 1, 0xaa: 1, 0xab: 1, 0xac: 1, 0xad: 1, 0xae: 1,
	// Tags de 2 bytes
	0x04: 2, 0x10: 2, 0x34: 2, 0x40: 2, 0x41: 2, 0x42: 2, 0x45: 2, 0x46: 2,
	0x54: 2, 0x55: 2, 0x56: 2, 0x57: 2, 0x58: 2, 0x59: 2, 0x60: 2, 0x61: 2,
	0x62: 2, 0x70: 2, 0x71: 2, 0x72: 2, 0x73: 2, 0x74: 2, 0x75: 2, 0x76: 2,
	0x77: 2, 0xb0: 2, 0xb1: 2, 0xb2: 2, 0xb3: 2, 0xb4: 2, 0xb5: 2, 0xb6: 2,
	0xb7: 2, 0xb8: 2, 0xb9: 2, 0xd6: 2, 0xd7: 2, 0xd8: 2, 0xd9: 2, 0xda: 2,
	// Tags de 3 bytes
	0x63: 3, 0x64: 3, 0x6f: 3, 0x5d: 3, 0x65: 3, 0x66: 3, 0x67: 3, 0x68: 3,
	0x69: 3, 0x6a: 3, 0x6b: 3, 0x6c: 3, 0x6d: 3, 0x6e: 3, 0xfa: 3,
	// Tags de 4 bytes
	0x20: 4, 0x33: 4, 0x44: 4, 0x90: 4, 0xc0: 4, 0xc2: 4, 0xc3: 4, 0xd3: 4,
	0xd4: 4, 0xdb: 4, 0xdc: 4, 0xdd: 4, 0xde: 4, 0xdf: 4, 0xf0: 4, 0xf9: 4,
	0x5a: 4, 0x47: 4, 0xf1: 4, 0xf2: 4, 0xf3: 4, 0xf4: 4, 0xf5: 4, 0xf6: 4,
	0xf7: 4, 0xf8: 4, 0xe2: 4, 0xe9: 4,
	// Tags especiais
	0x5b: 7,  // tamanho variável
	0x5c: 68, // PressurePro
	0xfd: 8,  // extended data
	0xfe: 8,  // extended tags
}

var tagNames = map[byte]string{
	0x01: "version_hw",
	0x02: "version_fw",
	0x03: "imei",
	0x04: "device_id",
	0x10: "index",
	0x20: "time",
	0x30: "coordinates",
	0x33: "speed_direction",
	0x34: "altitude",
	0x35: "hdop",
	0x40: "status",
	0x41: "power",
	0x42: "battery",
	0x43: "device_temp",
	0x44: "acceleration",
	0x45: "output",
	0x46: "input",
	0x48: "status_extended",
	0x58: "rs2320",
	0x59: "rs2321",
	0x90: "driver_id",
	0xc0: "fuel_total",
	0xc1: "fuel_rpm_temp",
	0xc2: "can_b0",
	0xc3: "can_b1",
	0xd4: "odometer",
	0xe0: "command_number",
	0xe1: "command_result",
	0xea: "user_data_array",
	0xfd: "extended_data",
	0xfe: "extended_tags",
}

func getTagLength(tag byte) int {
	if length, ok := tagLengthMap[tag]; ok {
		return length
	}
	return 2
}

func getTagName(tag byte) string {
	if name, ok := tagNames[tag]; ok {
		return name
	}
	return fmt.Sprintf("tag_0x%02x", tag)
}

// ============================================
// CONVERSOR DE POSITION PARA TELEMETRIA_RECORD
// ============================================

func convertToTelemetriaRecord(pos Position, currentIMEI string) TelemetriaRecord {
	imei := pos.DeviceID
	if imei == "" {
		imei = currentIMEI
	}

	record := TelemetriaRecord{
		IMEI:       imei,
		Timestamp:  pos.Time,
		Latitude:   pos.Latitude,
		Longitude:  pos.Longitude,
		Velocidade: float32(pos.Speed),
		Atributos:  make(map[string]interface{}),
	}

	// Copia atributos diretamente. O json.Marshal tratará os tipos.
	for k, v := range pos.Attributes {
		// Evita recursão ou campos redundantes
		if k == "time" || k == "timestamp" {
			continue
		}
		record.Atributos[k] = v
	}

	// Campos úteis garantidos como tipos básicos
	record.Atributos["altitude"] = pos.Altitude
	record.Atributos["course"] = pos.Course
	if pos.Alarm != "" {
		record.Atributos["alarm"] = pos.Alarm
	}
	record.Atributos["valid"] = pos.Valid

	return record
}

// ============================================
// SERVIDOR GALILEOSKY
// ============================================

var batchProcessor *BatchProcessor

func main() {
    // 1. Defina seus dados separadamente (limpo e sem erro de parsing)
    dbUser     := "postgres.uoovvwxnerpqeksuogvi"
    dbPassword := "r8xLrSZp7yY%25%23k%2A"
    dbHost     := "aws-1-sa-east-1.pooler.supabase.com"
    dbPort     := 6543
    dbName     := "postgres"

    // Altere sua connString para isto:
	connString := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=require&default_query_exec_mode=simple_protocol", dbUser, dbPassword, dbHost, dbPort, dbName)
	var err error
	batchProcessor, err = NewBatchProcessor(connString)
	if err != nil {
		fmt.Printf("❌ Erro fatal ao conectar ao Supabase: %v\n", err)
		return
	}
	defer batchProcessor.Close()

	// Inicia servidor TCP
	ln, err := net.Listen("tcp", "0.0.0.0:4040")
	if err != nil {
		fmt.Printf("Erro ao iniciar servidor: %v\n", err)
		return
	}
	defer ln.Close()

	fmt.Println("=== SERVIDOR GALILEOSKY v4.0 (Supabase + Batching) ===")
	fmt.Println("Aguardando conexões na porta 4040...")

	// Goroutine para mostrar estatísticas periodicamente
	go showStats()

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("Erro na conexão: %v\n", err)
			continue
		}
		go handleConnection(conn)
	}
}

func showStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	lastTotal := int64(0)
	lastTime := time.Now()
	
	for range ticker.C {
		if batchProcessor == nil {
			return
		}
		stats := batchProcessor.GetStats()
		
		// Calcula taxa atual
		currentTotal := stats.TotalRecords
		elapsed := time.Since(lastTime).Seconds()
		rate := float64(currentTotal-lastTotal) / elapsed
		
		fmt.Printf("\n📊 ESTATÍSTICAS SUPABASE:\n")
		fmt.Printf("   Total Registros: %d\n", stats.TotalRecords)
		fmt.Printf("   Total Batches: %d\n", stats.TotalBatches)
		fmt.Printf("   Batches com erro: %d\n", stats.FailedBatches)
		fmt.Printf("   Último batch: %d registros\n", stats.LastBatchSize)
		if stats.LastBatchTime.Unix() > 0 {
			fmt.Printf("   Último envio: %s\n", stats.LastBatchTime.Format("15:04:05"))
		}
		if stats.LastError != "" {
			fmt.Printf("   ⚠️  Último erro: %s\n", stats.LastError)
		}
		fmt.Printf("   📈 Taxa atual: %.1f registros/segundo\n", rate)
		fmt.Println()
		
		lastTotal = currentTotal
		lastTime = time.Now()
	}
}

// Versão modificada do handleConnection para enviar ao Supabase
func handleConnection(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr().String()
	fmt.Printf("[%s] Nova conexão estabelecida\n", addr)

	buffer := make([]byte, 4096)
	var pendingData []byte
	var currentIMEI string
	var packetCount int

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Printf("[%s] Conexão fechada: %v\n", addr, err)
			return
		}
		
		// DEBUG: Mostra os primeiros bytes recebidos
		if n > 0 && packetCount == 0 {
			fmt.Printf("[%s] Recebidos %d bytes, primeiros 20: %X\n", addr, n, buffer[:min(n, 20)])
		}

		pendingData = append(pendingData, buffer[:n]...)

		for len(pendingData) >= 4 {
			if pendingData[0] != 0x01 && pendingData[0] != 0x08 {
				pendingData = pendingData[1:]
				continue
			}

			lengthField := binary.LittleEndian.Uint16(pendingData[1:3])
			hasUnsentData := (lengthField & 0x8000) != 0
			packetLength := lengthField & 0x7FFF

			totalPacketSize := 1 + 2 + int(packetLength) + 2

			if len(pendingData) < totalPacketSize {
				break
			}

			packetData := pendingData[:totalPacketSize]
			pendingData = pendingData[totalPacketSize:]
			packetCount++
			
			fmt.Printf("[%s] Pacote #%d, Header: 0x%02X, Length: %d, TotalSize: %d\n", 
				addr, packetCount, packetData[0], packetLength, totalPacketSize)

			packet := parsePacket(packetData, hasUnsentData, int(packetLength))

			if packet.IsValid {
				fmt.Printf("[%s] Pacote VÁLIDO, %d posições encontradas\n", addr, len(packet.Positions))
				
				// Extrai IMEI do pacote se disponível
				for _, pos := range packet.Positions {
					if pos.DeviceID != "" {
						currentIMEI = pos.DeviceID
						fmt.Printf("[%s] 📱 IMEI capturado do pacote: %s\n", addr, currentIMEI)
					}
				}
				
				printPacketInfo(packet)

				// Dentro do handleConnection, no loop das posições
				for _, pos := range packet.Positions {
					// CRIA O REGISTRO PARA O SUPABASE USANDO A NOVA FUNÇÃO
					record := convertToTelemetriaRecord(pos, currentIMEI)

					// SÓ ENVIA SE TIVER IMEI E COORDENADAS VÁLIDAS
					if record.IMEI != "" && pos.Valid && (pos.Latitude != 0 || pos.Longitude != 0) {
						fmt.Printf("[%s] ✅ Enviando posição: IMEI=%s, Lat=%.6f, Lon=%.6f, Time=%s\n",
							addr, record.IMEI, record.Latitude, record.Longitude, record.Timestamp.Format("15:04:05"))
						batchProcessor.AddRecord(record)
					} else {
						fmt.Printf("[%s] ⚠️ Posição ignorada: IMEI=%s, Valid=%v, Lat=%.6f, Lon=%.6f\n",
							addr, record.IMEI, pos.Valid, record.Latitude, record.Longitude)
					}
				}

				sendAck(conn, packet.Checksum)
				fmt.Printf("[%s] ACK enviado\n", addr)
			} else {
				fmt.Printf("[%s] ❌ Pacote INVÁLIDO (checksum incorreto)\n", addr)
				conn.Write([]byte{0x15})
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parsePacket(data []byte, hasUnsentData bool, packetLength int) GalileoPacket {
	packet := GalileoPacket{
		Header:        data[0],
		HasUnsentData: hasUnsentData,
		IsValid:       false,
		Positions:     make([]Position, 0),
	}

	packet.Length = uint16(packetLength)
	packet.RawData = data[3 : len(data)-2]
	packet.Checksum = binary.LittleEndian.Uint16(data[len(data)-2:])

	calculatedChecksum := crc16Modbus(data[:len(data)-2])
	packet.IsValid = calculatedChecksum == packet.Checksum

	if !packet.IsValid {
		fmt.Printf("Checksum inválido: calculado=0x%04X, recebido=0x%04X\n",
			calculatedChecksum, packet.Checksum)
		return packet
	}

	fmt.Printf("Parsing packet header 0x%02X, raw data length: %d\n", packet.Header, len(packet.RawData))
	fmt.Printf("Raw data (primeiros 32 bytes): %X\n", packet.RawData[:min(32, len(packet.RawData))])

	// Processa baseado no tipo de header
	if packet.Header == 0x01 {
		packet.Positions = decodePositions(packet.RawData)
		fmt.Printf("Decodificadas %d posições do pacote 0x01\n", len(packet.Positions))
	} else if packet.Header == 0x08 {
		packet.Positions = decodeCompressedPositions(packet.RawData)
		fmt.Printf("Decodificadas %d posições do pacote 0x08\n", len(packet.Positions))
	} else {
		fmt.Printf("Header desconhecido: 0x%02X\n", packet.Header)
	}

	return packet
}

func decodePositions(data []byte) []Position {
	positions := make([]Position, 0)
	buf := NewByteBuffer(data)

	tags := make(map[byte]bool)
	var position *Position
	var deviceID string
	hasLocation := false
	tagCount := 0

	fmt.Printf("Decodificando posições, total de dados: %d bytes\n", len(data))

	for buf.Remaining() > 0 {
		if buf.Remaining() < 1 {
			break
		}

		tag := buf.ReadByte()
		tagCount++
		
		// Suprime logs de tags repetitivas para não poluir
		if tag != 0x00 && tag != 0x7F && tag != 0x80 {
			fmt.Printf("  Tag #%d: 0x%02X, remaining: %d\n", tagCount, tag, buf.Remaining())
		}

		// Se já vimos esta tag, é um novo registro
		if tags[tag] && tag != 0x00 { // Ignora tag 0x00 que é padding
			fmt.Printf("  Tag repetida 0x%02X, finalizando posição atual\n", tag)
			if hasLocation && position != nil && !position.Time.IsZero() {
				// Garante que o DeviceID seja preenchido
				if position.DeviceID == "" && deviceID != "" {
					position.DeviceID = deviceID
				}
				positions = append(positions, *position)
				fmt.Printf("  Posição adicionada: IMEI=%s, Lat=%.6f, Lon=%.6f, Time=%s\n", 
					position.DeviceID, position.Latitude, position.Longitude, position.Time.Format("15:04:05"))
			}
			tags = make(map[byte]bool)
			hasLocation = false
			position = nil
		}

		tags[tag] = true

		switch tag {
		case 0x03: // IMEI
			if buf.Remaining() >= 15 {
				deviceID = string(buf.ReadBytes(15))
				fmt.Printf("  📱 IMEI encontrado: %s\n", deviceID)
				// Se já temos uma posição, atualiza o DeviceID
				if position != nil {
					position.DeviceID = deviceID
				}
			}

		case 0x30: // Coordinates
			hasLocation = true
			if position == nil {
				position = &Position{
					DeviceID:   deviceID, // Pega o IMEI atual
					Attributes: make(map[string]interface{}),
				}
			}

			if buf.Remaining() >= 9 {
				flags := buf.ReadByte()
				position.Valid = (flags & 0xF0) == 0x00
				lat := int32(buf.ReadUint32())
				lon := int32(buf.ReadUint32())
				position.Latitude = float64(lat) / 1000000.0
				position.Longitude = float64(lon) / 1000000.0
				fmt.Printf("  📍 Coordenadas: flags=0x%02X, Lat=%.6f, Lon=%.6f, Valid=%v, IMEI=%s\n", 
					flags, position.Latitude, position.Longitude, position.Valid, position.DeviceID)
			} else {
				fmt.Printf("  ERRO: buffer insuficiente para coordenadas, remaining=%d\n", buf.Remaining())
			}

		default:
			if position == nil {
				position = &Position{
					DeviceID:   deviceID,
					Attributes: make(map[string]interface{}),
				}
			}
			decodeTag(position, buf, tag)
		}
	}

	// Adiciona último registro
	if hasLocation && position != nil && !position.Time.IsZero() {
		if position.DeviceID == "" && deviceID != "" {
			position.DeviceID = deviceID
		}
		positions = append(positions, *position)
		fmt.Printf("  Última posição adicionada: IMEI=%s, Lat=%.6f, Lon=%.6f\n", 
			position.DeviceID, position.Latitude, position.Longitude)
	}

	fmt.Printf("Total de posições decodificadas: %d\n", len(positions))
	return positions
}

func decodeCompressedPositions(data []byte) []Position {
	positions := make([]Position, 0)
	buf := NewByteBuffer(data)

	for buf.Remaining() > 2 {
		position := Position{
			Attributes: make(map[string]interface{}),
		}

		// Decodifica minimal data set (10 bytes)
		if !decodeMinimalDataSet(&position, buf) {
			break
		}

		// Lê lista de tags
		if buf.Remaining() < 1 {
			break
		}
		tagsCount := int(buf.ReadByte() & 0x7F) // Usa apenas 7 bits
		tags := make([]byte, tagsCount)

		for i := 0; i < tagsCount && buf.Remaining() > 0; i++ {
			tags[i] = buf.ReadByte()
		}

		// Decodifica cada tag
		for _, tag := range tags {
			decodeTag(&position, buf, tag)
		}

		positions = append(positions, position)
	}

	return positions
}

func decodeMinimalDataSet(position *Position, buf *ByteBuffer) bool {
	if buf.Remaining() < 10 {
		return false
	}

	data := buf.ReadBytes(10)
	bitBuf := NewBitBuffer(data)

	// Pula o primeiro bit (indicador de dados não enviados)
	bitBuf.ReadBits(1)

	// Lê timestamp (25 bits, segundos desde 01/01/1970)
	seconds := bitBuf.ReadBits(25)
	position.Time = time.Unix(int64(seconds), 0).UTC()

	// Validade da coordenada
	position.Valid = bitBuf.ReadBits(1) == 0

	// Longitude (22 bits)
	lonRaw := bitBuf.ReadBits(22)
	position.Longitude = 360.0 * float64(lonRaw) / 4194304.0 - 180.0

	// Latitude (21 bits)
	latRaw := bitBuf.ReadBits(21)
	position.Latitude = 180.0 * float64(latRaw) / 2097152.0 - 90.0

	// Alarme
	if bitBuf.ReadBits(1) > 0 {
		position.Alarm = "general"
	}

	return true
}

func decodeTag(position *Position, buf *ByteBuffer, tag byte) {
	// Tags ADC (0x50-0x57)
	if tag >= 0x50 && tag <= 0x57 {
		if buf.Remaining() >= 2 {
			position.Attributes[fmt.Sprintf("adc%d", tag-0x50)] = buf.ReadUint16()
		}
		return
	}

	// Tags Fuel (0x60-0x62)
	if tag >= 0x60 && tag <= 0x62 {
		if buf.Remaining() >= 2 {
			position.Attributes[fmt.Sprintf("fuel%d", tag-0x60)] = buf.ReadUint16()
		}
		return
	}

	// Tags CAN (0xa0-0xaf)
	if tag >= 0xa0 && tag <= 0xaf {
		if buf.Remaining() >= 1 {
			position.Attributes[fmt.Sprintf("can8BitR%d", tag-0xa0+15)] = buf.ReadByte()
		}
		return
	}

	// Tags CAN (0xb0-0xb9)
	if tag >= 0xb0 && tag <= 0xb9 {
		if buf.Remaining() >= 2 {
			position.Attributes[fmt.Sprintf("can16BitR%d", tag-0xb0+5)] = buf.ReadUint16()
		}
		return
	}

	// Tags CAN (0xc4-0xd2)
	if tag >= 0xc4 && tag <= 0xd2 {
		if buf.Remaining() >= 1 {
			position.Attributes[fmt.Sprintf("can8BitR%d", tag-0xc4)] = buf.ReadByte()
		}
		return
	}

	// Tags CAN (0xd6-0xda)
	if tag >= 0xd6 && tag <= 0xda {
		if buf.Remaining() >= 2 {
			position.Attributes[fmt.Sprintf("can16BitR%d", tag-0xd6)] = buf.ReadUint16()
		}
		return
	}

	// Tags CAN (0xdb-0xdf)
	if tag >= 0xdb && tag <= 0xdf {
		if buf.Remaining() >= 4 {
			position.Attributes[fmt.Sprintf("can32BitR%d", tag-0xdb)] = buf.ReadUint32()
		}
		return
	}

	// User Data (0xe2-0xe9)
	if tag >= 0xe2 && tag <= 0xe9 {
		if buf.Remaining() >= 4 {
			position.Attributes[fmt.Sprintf("userData%d", tag-0xe2)] = buf.ReadUint32()
		}
		return
	}

	// Tags CAN (0xf0-0xf9)
	if tag >= 0xf0 && tag <= 0xf9 {
		if buf.Remaining() >= 4 {
			position.Attributes[fmt.Sprintf("can32BitR%d", tag-0xf0+5)] = buf.ReadUint32()
		}
		return
	}

	// Tags específicas
	switch tag {
	case 0x01, 0x02:
		if buf.Remaining() >= 1 {
			position.Attributes[getTagName(tag)] = buf.ReadByte()
		}
	case 0x04, 0x10, 0x40, 0x41, 0x42, 0x45, 0x46, 0x58, 0x59:
		if buf.Remaining() >= 2 {
			val := buf.ReadUint16()
			if tag == 0x41 || tag == 0x42 {
				position.Attributes[getTagName(tag)] = float64(val) / 1000.0
			} else {
				position.Attributes[getTagName(tag)] = val
			}
		}
	case 0x20:
		if buf.Remaining() >= 4 {
			timestamp := buf.ReadUint32()
			position.Time = time.Unix(int64(timestamp), 0).UTC()
			position.Attributes[getTagName(tag)] = position.Time
		}
	case 0x33:
		if buf.Remaining() >= 4 {
			speed := float64(buf.ReadUint16()) * 0.1
			course := float64(buf.ReadUint16()) * 0.1
			position.Speed = speed
			position.Course = course
		}
	case 0x34:
		if buf.Remaining() >= 2 {
			position.Altitude = float64(int16(buf.ReadUint16()))
		}
	case 0x35:
		if buf.Remaining() >= 1 {
			position.Attributes["hdop"] = float64(buf.ReadByte()) * 0.1
		}
	case 0x43:
		if buf.Remaining() >= 1 {
			position.Attributes["device_temp"] = int8(buf.ReadByte())
		}
	case 0x44:
		if buf.Remaining() >= 4 {
			position.Attributes["acceleration"] = buf.ReadUint32()
		}
	case 0x48:
		if buf.Remaining() >= 2 {
			position.Attributes["status_extended"] = buf.ReadUint16()
		}
	case 0x90:
		if buf.Remaining() >= 4 {
			position.Attributes["driver_id"] = buf.ReadUint32()
		}
	case 0xc0:
		if buf.Remaining() >= 4 {
			position.Attributes["fuel_total"] = float64(buf.ReadUint32()) * 0.5
		}
	case 0xc1:
		if buf.Remaining() >= 4 {
			fuel := float64(buf.ReadByte()) * 0.4
			temp := float64(buf.ReadByte()) - 40
			rpm := float64(buf.ReadUint16()) * 0.125
			position.Attributes["fuel"] = fuel
			position.Attributes["temp"] = temp
			position.Attributes["rpm"] = rpm
		}
	case 0xc2, 0xc3:
		if buf.Remaining() >= 4 {
			position.Attributes[getTagName(tag)] = buf.ReadUint32()
		}
	case 0xd4:
		if buf.Remaining() >= 4 {
			position.Attributes["odometer"] = buf.ReadUint32()
		}
	case 0xe0:
		if buf.Remaining() >= 4 {
			position.Attributes["command_number"] = buf.ReadUint32()
		}
	case 0xe1:
		if buf.Remaining() >= 1 {
			length := int(buf.ReadByte())
			if buf.Remaining() >= length {
				position.Attributes["command_result"] = string(buf.ReadBytes(length))
			}
		}
	case 0xea:
		if buf.Remaining() >= 1 {
			length := int(buf.ReadByte())
			if buf.Remaining() >= length {
				position.Attributes["user_data_array"] = hex.EncodeToString(buf.ReadBytes(length))
			}
		}
	default:
		// Pula tags desconhecidas
		length := getTagLength(tag)
		if buf.Remaining() >= length {
			buf.Skip(length)
		}
	}
}

func printPacketInfo(packet GalileoPacket) {
	fmt.Printf("\n=== PACOTE GALILEOSKY ===\n")
	fmt.Printf("Header: 0x%02X\n", packet.Header)
	fmt.Printf("Length: %d bytes\n", packet.Length)
	fmt.Printf("Unsent Data: %v\n", packet.HasUnsentData)
	fmt.Printf("Checksum: 0x%04X (VÁLIDO)\n", packet.Checksum)
	fmt.Printf("Positions: %d\n", len(packet.Positions))

	for i, pos := range packet.Positions {
		fmt.Printf("\n--- POSIÇÃO %d ---\n", i+1)
		if pos.DeviceID != "" {
			fmt.Printf("  Device ID: %s\n", pos.DeviceID)
		}
		fmt.Printf("  Time: %s\n", pos.Time.Format("2006-01-02 15:04:05"))
		fmt.Printf("  Valid: %v\n", pos.Valid)
		fmt.Printf("  Latitude: %.6f\n", pos.Latitude)
		fmt.Printf("  Longitude: %.6f\n", pos.Longitude)
		fmt.Printf("  Speed: %.1f km/h\n", pos.Speed)
		fmt.Printf("  Course: %.1f°\n", pos.Course)
		fmt.Printf("  Altitude: %.0f m\n", pos.Altitude)
		if pos.Alarm != "" {
			fmt.Printf("  Alarm: %s\n", pos.Alarm)
		}
		if len(pos.Attributes) > 0 {
			fmt.Println("  Attributes:")
			for k, v := range pos.Attributes {
				fmt.Printf("    %s: %v\n", k, v)
			}
		}
	}
}

func sendAck(conn net.Conn, checksum uint16) {
	ack := []byte{0x02, byte(checksum & 0xFF), byte(checksum >> 8)}
	conn.Write(ack)
}

func crc16Modbus(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc = crc >> 1
			}
		}
	}
	return crc
}

// ByteBuffer helper
type ByteBuffer struct {
	data []byte
	pos  int
}

func NewByteBuffer(data []byte) *ByteBuffer {
	return &ByteBuffer{data: data, pos: 0}
}

func (b *ByteBuffer) Remaining() int {
	return len(b.data) - b.pos
}

func (b *ByteBuffer) ReadByte() byte {
	if b.pos >= len(b.data) {
		return 0
	}
	val := b.data[b.pos]
	b.pos++
	return val
}

func (b *ByteBuffer) ReadBytes(n int) []byte {
	if b.pos+n > len(b.data) {
		return nil
	}
	val := b.data[b.pos : b.pos+n]
	b.pos += n
	return val
}

func (b *ByteBuffer) ReadUint16() uint16 {
	if b.pos+2 > len(b.data) {
		return 0
	}
	val := binary.LittleEndian.Uint16(b.data[b.pos : b.pos+2])
	b.pos += 2
	return val
}

func (b *ByteBuffer) ReadUint32() uint32 {
	if b.pos+4 > len(b.data) {
		return 0
	}
	val := binary.LittleEndian.Uint32(b.data[b.pos : b.pos+4])
	b.pos += 4
	return val
}

func (b *ByteBuffer) Skip(n int) {
	b.pos += n
}

// BitBuffer helper
type BitBuffer struct {
	data []byte
	pos  int
	bit  int
}

func NewBitBuffer(data []byte) *BitBuffer {
	return &BitBuffer{data: data, pos: 0, bit: 0}
}

func (b *BitBuffer) ReadBits(n int) uint32 {
	var result uint32 = 0
	for i := 0; i < n; i++ {
		result <<= 1
		if b.pos < len(b.data) {
			bit := (b.data[b.pos] >> (7 - b.bit)) & 0x01
			result |= uint32(bit)
			b.bit++
			if b.bit >= 8 {
				b.bit = 0
				b.pos++
			}
		}
	}
	return result
}