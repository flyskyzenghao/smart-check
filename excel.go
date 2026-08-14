package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// ExcelRow Excel 中每条商机数据
type ExcelRow struct {
	ShangjiID    string `json:"shangji_id"`
	CustomerName string `json:"customer_name"`
	RowNumber    int    `json:"row_number"`
}

// ReadInputExcel 读取输入 Excel，返回商机列表和空行号列表
func ReadInputExcel(path string) ([]ExcelRow, []int, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		return nil, nil, fmt.Errorf("Excel 为空或读取失败")
	}

	// 解析表头
	headers := rows[0]
	colMap := make(map[string]int)
	for i, h := range headers {
		colMap[strings.TrimSpace(h)] = i
	}

	idxSJ, ok := colMap["商机id"]
	if !ok {
		return nil, nil, fmt.Errorf("Excel缺少\"商机id\"列，当前表头: %v", headers)
	}
	idxName, hasName := colMap["客户名称"]

	var customers []ExcelRow
	var emptyRows []int

	for rowIdx := 1; rowIdx < len(rows); rowIdx++ {
		row := rows[rowIdx]
		// 跳过全空行
		allEmpty := true
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			continue
		}

		rawID := ""
		if idxSJ < len(row) {
			rawID = strings.TrimSpace(row[idxSJ])
		}
		if rawID == "" {
			emptyRows = append(emptyRows, rowIdx+1) // 1-based
			continue
		}

		name := ""
		if hasName && idxName < len(row) {
			name = strings.TrimSpace(row[idxName])
		}

		customers = append(customers, ExcelRow{
			ShangjiID:    rawID,
			CustomerName: name,
			RowNumber:    rowIdx + 1, // 1-based
		})
	}

	return customers, emptyRows, nil
}

// WriteOutputExcel 将结果追加到原始 Excel，输出新文件
func WriteOutputExcel(inputPath, outputDir string, results []ResultItem) (string, error) {
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("商机分析结果_%s.xlsx", timestamp)
	outputPath := filepath.Join(outputDir, filename)

	f, err := excelize.OpenFile(inputPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	rows, _ := f.GetRows(sheet)

	// 找到最后一个有内容的列
	lastCol := 0
	if len(rows) > 0 {
		for i := len(rows[0]) - 1; i >= 0; i-- {
			if strings.TrimSpace(rows[0][i]) != "" {
				lastCol = i
				break
			}
		}
	}

	// 追加表头
	colResult, _ := excelize.CoordinatesToCellName(lastCol+2, 1)
	colChat, _ := excelize.CoordinatesToCellName(lastCol+3, 1)
	colSituation, _ := excelize.CoordinatesToCellName(lastCol+4, 1)
	colInterruption, _ := excelize.CoordinatesToCellName(lastCol+5, 1)
	colScenario, _ := excelize.CoordinatesToCellName(lastCol+6, 1)
	colRules, _ := excelize.CoordinatesToCellName(lastCol+7, 1)
	colRoles, _ := excelize.CoordinatesToCellName(lastCol+8, 1)
	f.SetCellValue(sheet, colResult, "批量介入审核结果")
	f.SetCellValue(sheet, colChat, "聊天记录")
	f.SetCellValue(sheet, colSituation, "介入情况")
	f.SetCellValue(sheet, colInterruption, "是否插话")
	f.SetCellValue(sheet, colScenario, "营销场景")
	f.SetCellValue(sheet, colRules, "命中关键条件")
	f.SetCellValue(sheet, colRoles, "参与角色")

	// 构建 row_number -> result 映射
	resultsMap := make(map[int]ResultItem)
	for _, r := range results {
		resultsMap[r.RowNumber] = r
	}

	// 遍历数据行写入结果
	for rowIdx := 1; rowIdx < len(rows); rowIdx++ {
		rowNum := rowIdx + 1 // 1-based
		if r, ok := resultsMap[rowNum]; ok {
			cellResult, _ := excelize.CoordinatesToCellName(lastCol+2, rowIdx+1)
			cellChat, _ := excelize.CoordinatesToCellName(lastCol+3, rowIdx+1)
			cellSituation, _ := excelize.CoordinatesToCellName(lastCol+4, rowIdx+1)
			cellInterruption, _ := excelize.CoordinatesToCellName(lastCol+5, rowIdx+1)
			cellScenario, _ := excelize.CoordinatesToCellName(lastCol+6, rowIdx+1)
			cellRules, _ := excelize.CoordinatesToCellName(lastCol+7, rowIdx+1)
			cellRoles, _ := excelize.CoordinatesToCellName(lastCol+8, rowIdx+1)
			f.SetCellValue(sheet, cellResult, r.Intervention)
			f.SetCellValue(sheet, cellChat, r.ChatText)
			f.SetCellValue(sheet, cellSituation, r.InterventionSituation)
			f.SetCellValue(sheet, cellInterruption, r.Interruption)
			f.SetCellValue(sheet, cellScenario, r.Scenario)
			f.SetCellValue(sheet, cellRules, r.MatchedRules)
			f.SetCellValue(sheet, cellRoles, r.Roles)
		}
	}

	if err := f.SaveAs(outputPath); err != nil {
		return "", err
	}
	return outputPath, nil
}
