package controllers

import (
	"bikincetak-api/erpnext"
	"bikincetak-api/models"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)


func AddToCart(c *fiber.Ctx) error {
	// 1. Ambil Customer ID dari Token
	userToken := c.Locals("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	customerID := fmt.Sprintf("%v", claims["customer_id"])

	var req models.AddToCartRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Format data tidak valid"})
	}
	if req.Qty <= 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Kuantitas minimal 1"})
	}

	reqVariantJSONBytes, _ := json.Marshal(req.VariantLainnya)
	reqVariantString := string(reqVariantJSONBytes)

	cartDocType := "Cart" 
	cartEndpoint := "/api/resource/" + url.PathEscape(cartDocType) + "/" + url.PathEscape(customerID)
	
	resCheck, err := erpnext.ERPNextReq("GET", cartEndpoint, nil)

	if err != nil || strings.Contains(string(resCheck), "exc_type") {
		newCartPayload := map[string]interface{}{
			"name":     customerID,
			"customer": customerID,
			"items": []map[string]interface{}{
				{
					"item_code":       req.ItemCode,
					"variant_name":    req.VariantName,
					"qty":             req.Qty,
					"price":           req.Price,
					"image_url":       req.ImageURL,
					"uom":             req.UOM,
					"notes":           req.Notes,
					"variant_lainnya": reqVariantString,
				},
			},
		}
		
		payloadBytes, _ := json.Marshal(newCartPayload)
		_, errPOST := erpnext.ERPNextReq("POST", "/api/resource/"+url.PathEscape(cartDocType), payloadBytes)
		
		if errPOST != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Gagal membuat keranjang baru di ERPNext"})
		}

		return c.JSON(fiber.Map{"message": "Barang berhasil ditambahkan ke keranjang!"})
	}

	var existingCart models.ERPNextCart
	json.Unmarshal(resCheck, &existingCart)

	itemMatched := false
	for i, item := range existingCart.Data.Items {
		if item.ItemCode == req.ItemCode && item.Notes == req.Notes && item.VariantLainnya == reqVariantString {
			existingCart.Data.Items[i].Qty += req.Qty
			existingCart.Data.Items[i].Price = req.Price
			itemMatched = true
			break
		}
	}

	if !itemMatched {
		newItem := models.ERPNextCartItem{
			ItemCode:       req.ItemCode,
			VariantName:    req.VariantName,
			Qty:            req.Qty,
			Price:          req.Price,
			ImageURL:       req.ImageURL,
			UOM:            req.UOM,
			Notes:          req.Notes,
			VariantLainnya: reqVariantString,
		}
		existingCart.Data.Items = append(existingCart.Data.Items, newItem)
	}

	updatePayload := map[string]interface{}{
		"items": existingCart.Data.Items,
	}
	updateBytes, _ := json.Marshal(updatePayload)
	
	_, errPUT := erpnext.ERPNextReq("PUT", cartEndpoint, updateBytes)
	if errPUT != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Gagal memperbarui keranjang di ERPNext"})
	}

	return c.JSON(fiber.Map{"message": "Keranjang berhasil diperbarui!"})
}

func GetCart(c *fiber.Ctx) error {
	userToken := c.Locals("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	customerID := fmt.Sprintf("%v", claims["customer_id"])

	cartDocType := "Cart"
	cartEndpoint := "/api/resource/" + url.PathEscape(cartDocType) + "/" + url.PathEscape(customerID)

	res, err := erpnext.ERPNextReq("GET", cartEndpoint, nil)

	if err != nil || strings.Contains(string(res), "exc_type") {
		return c.JSON(fiber.Map{
			"message": "Keranjang kosong",
			"data": fiber.Map{
				"items": []interface{}{},
				"total": 0,
			},
		})
	}

	var cart models.ERPNextCart
	json.Unmarshal(res, &cart)

	var grandTotal float64
	var formattedItems []map[string]interface{}

	for _, item := range cart.Data.Items {
		var variants []models.CartVariantLainnya
		if item.VariantLainnya != "" {
			json.Unmarshal([]byte(item.VariantLainnya), &variants)
		}

		hargaItem := item.Price
		for _, addon := range variants {
			hargaItem += addon.Price
		}
		grandTotal += (hargaItem * float64(item.Qty))

		formattedItems = append(formattedItems, map[string]interface{}{
			"id":              item.Name, 
			"item_code":       item.ItemCode,
			"variant_name":    item.VariantName,
			"qty":             item.Qty,
			"price":           item.Price,
			"image_url":       item.ImageURL,
			"uom":             item.UOM,
			"notes":           item.Notes,
			"variant_lainnya": variants,
		})
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items": formattedItems,
			"total": grandTotal,
		},
	})
}



func UpdateCartItem(c *fiber.Ctx) error {
	itemID := c.Params("id") 
	if itemID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "ID Barang tidak boleh kosong"})
	}

	userToken := c.Locals("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	customerID := fmt.Sprintf("%v", claims["customer_id"])

	var req models.UpdateCartRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Format data tidak valid"})
	}

	if req.Qty <= 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Kuantitas minimal 1. Gunakan tombol hapus jika ingin menghapus barang."})
	}

	cartDocType := "Cart" 
	cartEndpoint := "/api/resource/" + url.PathEscape(cartDocType) + "/" + url.PathEscape(customerID)

	resCheck, err := erpnext.ERPNextReq("GET", cartEndpoint, nil)
	if err != nil || strings.Contains(string(resCheck), "exc_type") {
		return c.Status(404).JSON(fiber.Map{"error": "Keranjang tidak ditemukan"})
	}

	var existingCart models.ERPNextCart
	json.Unmarshal(resCheck, &existingCart)

	itemFound := false
	for i, item := range existingCart.Data.Items {
		if item.Name == itemID {
			existingCart.Data.Items[i].Qty = req.Qty
			itemFound = true
			break
		}
	}

	if !itemFound {
		return c.Status(404).JSON(fiber.Map{"error": "Barang tidak ditemukan di keranjang"})
	}

	updatePayload := map[string]interface{}{
		"items": existingCart.Data.Items,
	}
	updateBytes, _ := json.Marshal(updatePayload)

	resPUT, errPUT := erpnext.ERPNextReq("PUT", cartEndpoint, updateBytes)
	if errPUT != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Gagal memperbarui keranjang di server"})
	}

	if strings.Contains(string(resPUT), "exc_type") {
		fmt.Println("[ERP ERROR PUT]:", string(resPUT))
		return c.Status(400).JSON(fiber.Map{
			"error": "Gagal mengupdate kuantitas di ERPNext",
		})
	}

	return c.JSON(fiber.Map{
		"message": "Kuantitas barang berhasil diupdate",
	})
}

func DeleteCartItem(c *fiber.Ctx) error {
	itemID := c.Params("id") 
	if itemID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "ID Barang tidak boleh kosong"})
	}

	userToken := c.Locals("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	customerID := fmt.Sprintf("%v", claims["customer_id"])

	cartDocType := "Cart"
	cartEndpoint := "/api/resource/" + url.PathEscape(cartDocType) + "/" + url.PathEscape(customerID)

	resCheck, err := erpnext.ERPNextReq("GET", cartEndpoint, nil)
	if err != nil || strings.Contains(string(resCheck), "exc_type") {
		return c.Status(404).JSON(fiber.Map{"error": "Keranjang tidak ditemukan"})
	}

	var existingCart models.ERPNextCart
	json.Unmarshal(resCheck, &existingCart)

	var newItems []models.ERPNextCartItem
	itemFound := false

	for _, item := range existingCart.Data.Items {
		if item.Name == itemID {
			itemFound = true
			continue 
		}
		newItems = append(newItems, item)
	}

	if !itemFound {
		return c.Status(404).JSON(fiber.Map{"error": "Barang tidak ditemukan di keranjang"})
	}


	updatePayload := map[string]interface{}{
		"items": newItems,
	}
	updateBytes, _ := json.Marshal(updatePayload)

	_, errPUT := erpnext.ERPNextReq("PUT", cartEndpoint, updateBytes)
	if errPUT != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Gagal menghapus barang dari server"})
	}

	return c.JSON(fiber.Map{
		"message": "Barang berhasil dihapus dari keranjang",
	})
}