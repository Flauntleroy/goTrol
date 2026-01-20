package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const version = "1.0.0"

func main() {
	for {
		clearScreen()
		printBanner()
		showMainMenu()

		choice := readInput("Pilihan Anda: ")

		switch choice {
		case "1":
			runService()
		case "2":
			runBatchWithDate("autoorder", "Batch Auto Order")
		case "3":
			runBatchWithDate("updatewaktu", "Batch Update Waktu")
		case "4":
			runBatchWithDate("all", "Batch All (Auto Order + Update Waktu)")
		case "5":
			runBatchWithDate("retrytask3", "Retry Task 3 yang Gagal")
		case "6":
			runDashboard()
		case "0":
			fmt.Println("\n👋 Sampai jumpa!")
			os.Exit(0)
		default:
			fmt.Println("\n❌ Pilihan tidak valid. Tekan Enter untuk kembali...")
			waitEnter()
		}
	}
}

func clearScreen() {
	cmd := exec.Command("cmd", "/c", "cls")
	cmd.Stdout = os.Stdout
	cmd.Run()
}

func printBanner() {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║               GoTrol - Interactive Launcher                  ║")
	fmt.Printf("║                       Version %-7s                        ║\n", version)
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func showMainMenu() {
	fmt.Println("Pilih operasi yang ingin dijalankan:")
	fmt.Println()
	fmt.Println("  [1] 🚀 Run Service (Background Monitoring)")
	fmt.Println("  [2] 📋 Batch Auto Order")
	fmt.Println("  [3] 📤 Batch Update Waktu (Kirim ke BPJS)")
	fmt.Println("  [4] ⚡ Batch All (Auto Order + Update Waktu)")
	fmt.Println("  [5] 🔄 Retry Task 3 yang Gagal")
	fmt.Println("  [6] 📊 Buka Dashboard")
	fmt.Println()
	fmt.Println("  [0] ❌ Keluar")
	fmt.Println()
}

func showDateMenu() string {
	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)

	fmt.Println()
	fmt.Println("Pilih tanggal:")
	fmt.Println()
	fmt.Printf("  [1] 📅 Hari Ini (%s)\n", today.Format("2006-01-02"))
	fmt.Printf("  [2] 📆 Kemarin (%s)\n", yesterday.Format("2006-01-02"))
	fmt.Println("  [3] ✏️  Input Manual (YYYY-MM-DD)")
	fmt.Println()
	fmt.Println("  [0] ⬅️  Kembali")
	fmt.Println()

	choice := readInput("Pilihan Anda: ")

	switch choice {
	case "1":
		return today.Format("2006-01-02")
	case "2":
		return yesterday.Format("2006-01-02")
	case "3":
		return inputCustomDate()
	case "0":
		return ""
	default:
		fmt.Println("\n❌ Pilihan tidak valid.")
		waitEnter()
		return ""
	}
}

func inputCustomDate() string {
	fmt.Println()
	date := readInput("Masukkan tanggal (YYYY-MM-DD): ")

	// Validate date format
	_, err := time.Parse("2006-01-02", date)
	if err != nil {
		fmt.Println("\n❌ Format tanggal tidak valid! Gunakan format YYYY-MM-DD")
		fmt.Println("   Contoh: 2026-01-21")
		waitEnter()
		return ""
	}

	return date
}

func runService() {
	clearScreen()
	printBanner()
	fmt.Println("🚀 Menjalankan Background Service...")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("⚠️  Service akan berjalan terus-menerus.")
	fmt.Println("   Tekan Ctrl+C untuk menghentikan.")
	fmt.Println()

	cmd := exec.Command("./GoTrol.exe", "run")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Run()

	waitEnter()
}

func runBatchWithDate(batchType, title string) {
	clearScreen()
	printBanner()
	fmt.Printf("📋 %s\n", title)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	date := showDateMenu()
	if date == "" {
		return
	}

	clearScreen()
	printBanner()
	fmt.Printf("📋 %s\n", title)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Printf("📅 Tanggal: %s\n", date)
	fmt.Println()

	confirm := readInput("Lanjutkan? (y/n): ")
	if strings.ToLower(confirm) != "y" {
		fmt.Println("\n❌ Dibatalkan.")
		waitEnter()
		return
	}

	fmt.Println()
	fmt.Println("⏳ Memproses...")
	fmt.Println()

	cmd := exec.Command("./GoTrol.exe", "batch", batchType, "--date", date)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ Selesai! Tekan Enter untuk kembali ke menu...")
	waitEnter()
}

func runDashboard() {
	clearScreen()
	printBanner()
	fmt.Println("📊 Membuka Dashboard...")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("⚠️  Dashboard akan berjalan di http://localhost:8899")
	fmt.Println("   Tekan Ctrl+C untuk menghentikan.")
	fmt.Println()

	cmd := exec.Command("./GoTrolDashboard.exe")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Run()

	waitEnter()
}

func readInput(prompt string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(prompt)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func waitEnter() {
	reader := bufio.NewReader(os.Stdin)
	reader.ReadString('\n')
}
