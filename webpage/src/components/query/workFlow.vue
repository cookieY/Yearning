<template>
  <div>
    <Row>
      <Card>
        <div class="step-header-con">
          <h3>{{ stepData.title }}</h3>
          <h5>{{ stepData.describe }}</h5>
        </div>
        <p class="step-content"></p>
        <Row>
          <i-col span="8">
            <Alert type="warning" show-icon>
              注意事项:
              <span slot="desc">
              1.必须填写查询说明
              <br>
              2.根据查询条件预估所需的查询时间
              <br>
              3.所有提交的查询语句均会进行审计记录
              <br>
              4.仅支持select语句,不可使用非查询语句
              <br>
              5.已限制最大limit数，如自己输入的limit数大于平台配置的最大limit数则已平台配置的Limit数为准
            </span>
            </Alert>
          </i-col>
          <i-col span="12">
            <Form ref="step" :model="step" :rules="stepRules" :label-width="150">
              <FormItem label="机房:" prop="computer_room">
                <Select v-model="step.computer_room" @on-change="Connection_Name">
                  <Option v-for="i in datalist.computer_roomlist" :key="i" :value="i">{{i}}</Option>
                </Select>
              </FormItem>

              <FormItem label="连接名:" prop="connection_name">
                <Select v-model="step.connection_name" filterable>
                  <Option v-for="i in datalist.connection_name_list" :value="i.connection_name"
                          :key="i.connection_name">{{ i.connection_name }}
                  </Option>
                </Select>
              </FormItem>
              <FormItem label="审核人:" prop="person">
                <Select v-model="step.person" filterable>
                  <Option v-for="i in personlist" :value="i" :key="i">{{ i }}</Option>
                </Select>
              </FormItem>
              <FormItem label="是否需要导出数据:" prop="export">
                <RadioGroup v-model="step.export">
                  <Radio label="1">是</Radio>
                  <Radio label="0">否</Radio>
                </RadioGroup>
              </FormItem>
              <FormItem label="查询说明：" prop="opinion">
                <Input v-model="step.opinion" type="textarea" :autosize="{minRows: 4,maxRows: 8}"
                       placeholder="请填写查询说明"/>
              </FormItem>
              <FormItem label="">
                <Button @click="handleSubmit" style="width:100px;" type="primary">提交</Button>
              </FormItem>
            </Form>
          </i-col>
        </Row>
        <Steps style="margin-left: 10%">
          <Step v-for="item in stepList1" :title="item.title" :content="item.describe" :key="item.title"></Step>
        </Steps>
      </Card>
    </Row>
  </div>
</template>

<script>
  //
  import axios from 'axios'

  export default {
    name: 'work_flow',
    props: ['msg'],
    data () {
      return {
        stepData: {
          title: 'Yearning SQL查询系统',
          describe: `欢迎你！ ${sessionStorage.getItem('user')}`
        },
        step: {
          remark: '',
          computer_room: '',
          connection_name: '',
          person: '',
          export: '0'
        },
        stepList1: [
          {
            title: '提交',
            describe: '提交查询申请'
          },
          {
            title: '审核',
            describe: '等待审核结果'
          },
          {
            title: '查询',
            describe: '审核完毕，进入查询页面'
          }
        ],
        stepRules: {
          opinion: [
            {required: true, message: '请填写查询说明', trigger: 'blur'}
          ],
          computer_room: [{
            required: true,
            message: '机房地址不得为空',
            trigger: 'change'
          }],
          connection_name: [{
            required: true,
            message: '连接名不得为空',
            trigger: 'change'
          }],
          basename: [{
            required: true,
            message: '数据库名不得为空',
            trigger: 'change'
          }],
          person: [{
            required: true,
            message: '审核人不得为空',
            trigger: 'change'
          }]
        },
        item: {},
        personlist: [],
        datalist: {
          connection_name_list: [],
          basenamelist: [],
          sqllist: [],
          computer_roomlist: this.$config.computer_room
        }
      }
    },
    methods: {
      Connection_Name (val) {
        this.datalist.connection_name_list = []
        this.datalist.basenamelist = []
        this.step.connection_name = ''
        this.step.basename = ''
        if (val) {
          this.ScreenConnection(val)
        }
      },
      ScreenConnection (val) {
        this.datalist.connection_name_list = this.item.filter(item => {
          if (item.computer_room === val) {
            return item
          }
        })
      },
      handleSubmit () {
        this.$refs['step'].validate((valid) => {
          if (valid) {
            axios.put(`${this.$config.url}/query_worklf`, {
              'mode': 'put',
              'instructions': this.step.opinion,
              'connection_name': this.step.connection_name,
              'computer_room': this.step.computer_room,
              'export': this.step.export,
              'audit': this.step.person,
              'real_name': sessionStorage.getItem('real_name')
            })
              .then(() => {
                this.$router.push({
                  name: 'queryready'
                })
              })
          }
        })
      }
    },
    mounted () {
      axios.put(`${this.$config.url}/workorder/connection`, {'permissions_type': 'query'})
        .then(res => {
          this.item = res.data['connection']
          this.personlist = res.data['assigend']
          this.datalist.computer_roomlist = res.data['custom']
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
      axios.put(`${this.$config.url}/query_worklf`, {'mode': 'status'})
        .then(res => {
          if (res.data === 1) {
            this.$router.push({
              name: 'querypage'
            })
          } else if (res.data === 2) {
            this.$router.push({
              name: 'queryready'
            })
          }
        })
    }
  }
</script>

<style lang="less">
  .step {
    &-header-con {
      text-align: center;
      h3 {
        margin: 10px 0;
      }
      h5 {
        margin: 0 0 5px;
      }
    }
    &-content {
      padding: 5px 20px 26px;
      margin-bottom: 20px;
      border-bottom: 1px solid #dbdddf;
    }
    &-form {
      padding-bottom: 10px;
      border-bottom: 1px solid #dbdddf;
      margin-bottom: 20px;
    }
  }
</style>
