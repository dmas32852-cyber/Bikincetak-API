package controllers

import (
	"bikincetak-api/config"
	"bikincetak-api/database"
	"bikincetak-api/erpnext"
	"bikincetak-api/models"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/snap"
	"github.com/redis/go-redis/v9"
)


func CreateOrder(c *fiber.Ctx) error {
	var req models.CheckoutRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Format request tidak valid"})
	}

	if req.AddressName == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Alamat pengiriman harus dipilih"})
	}

	if len(req.SelectedItemIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Pilih minimal satu barang untuk dicheckout"})
	}

	userToken := c.Locals("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	customerEmail := fmt.Sprintf("%v", claims["email"])
	customerID := fmt.Sprintf("%v", claims["customer_id"])

	var wg sync.WaitGroup
	var erpAddress models.ERPNextAddressResponse
	var existingCart models.ERPNextCart
	var customerName string
	var errFetch error
	var mu sync.Mutex 

	wg.Add(3) 

	go func() {
		defer wg.Done()
		resAddress, errAddr := erpnext.ERPNextReq("GET", "/api/resource/Address/"+url.PathEscape(req.AddressName), nil)
		if errAddr != nil {
			mu.Lock(); errFetch = errAddr; mu.Unlock()
			return
		}
		json.Unmarshal(resAddress, &erpAddress)
	}()

	go func() {
		defer wg.Done()
		resCust, errCust := erpnext.ERPNextReq("GET", "/api/resource/Customer/"+url.PathEscape(customerID), nil)
		if errCust != nil {
			mu.Lock(); errFetch = errCust; mu.Unlock()
			return
		}
		var custData map[string]interface{}
		if err := json.Unmarshal(resCust, &custData); err == nil {
			if data, ok := custData["data"].(map[string]interface{}); ok {
				if name, ok := data["customer_name"].(string); ok {
					customerName = name
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		cartEndpoint := "/api/resource/Cart/" + url.PathEscape(customerID)
		resCart, errCart := erpnext.ERPNextReq("GET", cartEndpoint, nil)
		if errCart != nil || strings.Contains(string(resCart), "exc_type") {
			mu.Lock(); errFetch = fmt.Errorf("keranjang kosong"); mu.Unlock()
			return
		}
		json.Unmarshal(resCart, &existingCart)
	}()

	wg.Wait()

	if errFetch != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Gagal memproses pesanan. Pastikan keranjang dan alamat valid."})
	}

	if len(existingCart.Data.Items) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Keranjang belanja Anda kosong"})
	}

	var grossAmount int64 = 0
	var midtransItems []midtrans.ItemDetails
	var erpItems []map[string]interface{}

	for _, item := range existingCart.Data.Items {
	
		isItemPicked := false
		for _, pickedID := range req.SelectedItemIDs {
			if item.Name == pickedID {
				isItemPicked = true
				break
			}
		}

		if !isItemPicked {
			continue
		}

		baseQty := int32(item.Qty)
		basePrice := int64(item.Price)

		grossAmount += basePrice * int64(baseQty)

		midtransItems = append(midtransItems, midtrans.ItemDetails{
			ID:    item.ItemCode,
			Name:  item.VariantName, 
			Price: basePrice,
			Qty:   baseQty,
		})

		erpItems = append(erpItems, map[string]interface{}{
			"item_code": item.ItemCode,
			"qty":       item.Qty,
			"rate":      item.Price,
		})

		var variants []models.CartVariantLainnya
		if item.VariantLainnya != "" {
			json.Unmarshal([]byte(item.VariantLainnya), &variants)
		}

		for _, addon := range variants {
			addonPrice := int64(addon.Price)
			addonQty := baseQty 

			grossAmount += addonPrice * int64(addonQty)

			midtransItems = append(midtransItems, midtrans.ItemDetails{
				ID:    addon.ItemCode,
				Name:  addon.NameVariant,
				Price: addonPrice,
				Qty:   addonQty,
			})

			erpItems = append(erpItems, map[string]interface{}{
				"item_code": addon.ItemCode,
				"qty":       float64(addonQty),
				"rate":      addon.Price,
			})
		}
	}

	if len(erpItems) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Barang yang dipilih tidak ditemukan di keranjang"})
	}

	addr := erpAddress.Data
	if customerName == "" {
		customerName = customerID
	}

	deliveryDate := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	soPayload := map[string]interface{}{
		"customer":         customerID,
		"items":            erpItems,
		"customer_address": req.AddressName,
		"delivery_date":    deliveryDate,
	}

	tempOrderID := fmt.Sprintf("TRX-%d", time.Now().Unix())
	soPayloadBytes, _ := json.Marshal(soPayload)
	redisKey := "order_payload:" + tempOrderID

	if errSet := database.Rdb.Set(database.Ctx, redisKey, soPayloadBytes, 24*time.Hour).Err(); errSet != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Gagal menyimpan sesi pesanan"})
	}

	billAddress := &midtrans.CustomerAddress{
		FName:       customerName,
		Phone:       addr.Phone,
		Address:     addr.AddressLine1,
		City:        addr.City,
		Postcode:    addr.Pincode,
		CountryCode: "IDN",
	}

	snapReq := &snap.Request{
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  tempOrderID,
			GrossAmt: grossAmount,
		},
		Items:          &midtransItems,
		CustomerDetail: &midtrans.CustomerDetails{
			FName:    customerName,
			Email:    customerEmail,
			Phone:    addr.Phone,
			BillAddr: billAddress,
			ShipAddr: billAddress, 
		},
	}

	snapResp, errMidtrans := config.SnapClient.CreateTransaction(snapReq)
	if errMidtrans != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Gagal terhubung ke gateway pembayaran"})
	}

	go func() {
		var remainingItems []models.ERPNextCartItem
		
		for _, item := range existingCart.Data.Items {
			isPaid := false
			for _, pickedID := range req.SelectedItemIDs {
				if item.Name == pickedID {
					isPaid = true
					break
				}
			}
			
			if !isPaid {
				remainingItems = append(remainingItems, item)
			}
		}

		cartEndpoint := "/api/resource/Cart/" + url.PathEscape(customerID)
		
		if len(remainingItems) == 0 {
			erpnext.ERPNextReq("DELETE", cartEndpoint, nil)
		} else {
			updatePayload := map[string]interface{}{"items": remainingItems}
			updateBytes, _ := json.Marshal(updatePayload)
			erpnext.ERPNextReq("PUT", cartEndpoint, updateBytes)
		}
	}()

	return c.Status(200).JSON(fiber.Map{
		"message":     "Pesanan berhasil dibuat",
		"order_id":    tempOrderID,
		"payment_url": snapResp.RedirectURL,
		"snap_token":  snapResp.Token,
	})
}


func MidtransWebhook(c *fiber.Ctx) error {
	var notification map[string]interface{}
	if err := c.BodyParser(&notification); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Format notifikasi tidak valid"})
	}

	orderID, _ := notification["order_id"].(string)
	transactionStatus, _ := notification["transaction_status"].(string)

	if orderID == "" || transactionStatus == "" {
		return c.SendStatus(200)
	}

	if transactionStatus == "capture" || transactionStatus == "settlement" {
		redisKey := "order_payload:" + orderID

		cachedData, err := database.Rdb.Get(database.Ctx, redisKey).Result()
		if err == redis.Nil {
			fmt.Println("Webhook masuk, tapi data Redis tidak ada untuk:", orderID)
			return c.SendStatus(200)
		} else if err != nil {
			fmt.Println("Error baca Redis di Webhook:", err)
			return c.SendStatus(200)
		}

		var payloadMap map[string]interface{}
		json.Unmarshal([]byte(cachedData), &payloadMap)


		payloadMap["po_no"] = orderID


		finalPayloadBytes, _ := json.Marshal(payloadMap)

		resSO, errSO := erpnext.ERPNextReq("POST", "/api/resource/Sales Order", finalPayloadBytes)

		if errSO != nil || strings.Contains(string(resSO), "exc_type") {
			fmt.Println("GAGAL MEMBUAT SO DARI WEBHOOK. Respons ERPNext:", string(resSO))
			return c.SendStatus(200)
		}

		database.Rdb.Del(database.Ctx, redisKey)

		fmt.Println("[SUKSES] Pesanan Lunas via Midtrans! Draft SO Berhasil Dibuat dengan Ref:", orderID)
	}

	return c.SendStatus(200)
}